package core

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/observe"
	"github.com/strace-me/lotsman/pkg/registry"
)

type recorded struct {
	service, recipe string
	ok              bool
	calls           int
}

func execWithCanary(probeOK bool, rec *recorded) *zapretExec {
	return &zapretExec{
		// Still on the rung, so a verdict is legitimate.
		active: func() []registry.Service { return []registry.Service{{Name: "youtube"}, {Name: "discord"}} },
		canary: func(context.Context, string) (bool, string) {
			if probeOK {
				return true, ""
			}
			return false, "the path connects but carries nothing"
		},
		record: func(service, recipe string, ok bool) {
			rec.service, rec.recipe, rec.ok = service, recipe, ok
			rec.calls++
		},
		log: slog.New(slog.DiscardHandler),
	}
}

func TestCanaryDemotesAStrategyThatDidNotHelp(t *testing.T) {
	// The catalog holds both a recipe that does nothing against a given DPI and
	// one that defeats it, one line apart. A cold KB scores them equally and the
	// picker takes catalog order, so without this verdict the client would keep
	// choosing the broken one forever with the working one right behind it.
	var rec recorded
	z := execWithCanary(false, &rec)

	z.judge(context.Background(), "youtube", map[string]string{"youtube": "loser-recipe"})

	if rec.calls != 1 {
		t.Fatalf("outcome recorded %d times, want 1", rec.calls)
	}
	if rec.recipe != "loser-recipe" || rec.service != "youtube" {
		t.Errorf("recorded %s/%s, want youtube/loser-recipe", rec.service, rec.recipe)
	}
	if rec.ok {
		t.Error("a failed canary must be recorded as a FAILURE, or the picker keeps choosing it")
	}
}

func TestCanaryPromotesAStrategyThatWorked(t *testing.T) {
	var rec recorded
	z := execWithCanary(true, &rec)

	z.judge(context.Background(), "youtube", map[string]string{"youtube": "winner"})

	if rec.calls != 1 || !rec.ok || rec.recipe != "winner" {
		t.Errorf("recorded %+v, want one success for winner", rec)
	}
}

func TestCanarySkipsServicesItDidNotCompose(t *testing.T) {
	// Enable names one service, but the plan covers every service on the rung.
	// Judging one we did not choose a recipe for would attribute a verdict to the
	// wrong strategy.
	var rec recorded
	z := execWithCanary(false, &rec)

	z.judge(context.Background(), "discord", map[string]string{"youtube": "some-recipe"})

	if rec.calls != 0 {
		t.Errorf("recorded %d verdicts for a service with no chosen recipe, want 0", rec.calls)
	}
}

func TestCanarySkipsAServiceThatLeftTheRung(t *testing.T) {
	// The third consecutive failure escalates the service off the desync rung on
	// the very tick that spawned this judge. Probing 3s later then measures the
	// tunnel it moved to, and recording that would file a SUCCESS for the recipe
	// whose failures caused the escalation — promoting the loser.
	var rec recorded
	z := execWithCanary(true, &rec)
	z.active = func() []registry.Service { return []registry.Service{{Name: "discord"}} }

	z.judge(context.Background(), "youtube", map[string]string{"youtube": "r"})

	if rec.calls != 0 {
		t.Error("a service no longer on the rung must not be judged — the probe measures whatever it moved to")
	}
}

func TestCanaryIsInertWithoutAProbe(t *testing.T) {
	var rec recorded
	z := &zapretExec{
		active: func() []registry.Service { return []registry.Service{{Name: "youtube"}} },
		record: func(string, string, bool) { rec.calls++ }, log: slog.New(slog.DiscardHandler),
	}

	z.judge(context.Background(), "youtube", map[string]string{"youtube": "r"})

	if rec.calls != 0 {
		t.Error("with no way to probe, inventing a verdict would poison the KB")
	}
}

func TestCanaryRespectsCancellation(t *testing.T) {
	var rec recorded
	z := execWithCanary(true, &rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	z.judge(ctx, "youtube", map[string]string{"youtube": "r"})

	if rec.calls != 0 {
		t.Error("a cancelled shutdown must not record a verdict measured against a torn-down data plane")
	}
}

// The canary's verdict must reach the BRAIN, not only the picker. Demotion
// reorders candidates; it cannot move a service off a rung, and the brain listens
// to a probe that is blind to the failure the canary measures.
func TestStallReasonOnlyWhileOnTheDesyncRung(t *testing.T) {
	onRung := true
	z := &zapretExec{
		log: slog.New(slog.DiscardHandler),
		active: func() []registry.Service {
			if onRung {
				return []registry.Service{{Name: "youtube"}}
			}
			return nil
		},
	}

	if _, stalled, _ := z.StallReason("youtube"); stalled {
		t.Error("no verdict yet must not stall the probe")
	}

	z.noteCarrying("youtube", false, "the path connects but carries nothing")
	reason, stalled, _ := z.StallReason("youtube")
	if !stalled {
		t.Fatal("a canary that measured no goodput must fail the probe so the brain escalates")
	}
	if !strings.Contains(reason, "carries nothing") {
		t.Errorf("the reason must say what was observed, got %q", reason)
	}

	// Once the brain HAS escalated, the service rides a tunnel this canary never
	// measured. Continuing to fail its probe would walk it off a path there is no
	// evidence against — the same unmeasured-verdict family, self-inflicted.
	// Consumed, not held: a standing verdict failed every later probe until the
	// next canary passed, so three "consecutive failures" could all come from one
	// canary. The probe has to stay a second opinion, not an echo.
	if _, stalled, _ := z.StallReason("youtube"); stalled {
		t.Error("the verdict must be consumed by the first probe that reads it")
	}

	onRung = false
	z.noteCarrying("youtube", false, "the path connects but carries nothing")
	if _, stalled, _ := z.StallReason("youtube"); stalled {
		t.Error("the verdict must not follow the service off the desync rung")
	}

	onRung = true
	z.noteCarrying("youtube", true, "")
	if _, stalled, _ := z.StallReason("youtube"); stalled {
		t.Error("a recipe that carries again must clear the verdict")
	}
}

// The activity oracle exists because a probe was measured wrong in BOTH
// directions on the same machine within a day. This is the half that costs a
// working session: Discord's gateway URL timed out for minutes while 3.5 MB of
// voice flowed through the same rule.
func TestCarryingRequiresMovementNotMerelyFlows(t *testing.T) {
	c := &Core{carrying: observe.DefaultCarrying()}
	set := func(bytes int64, flows int, stalled float64) {
		c.obsMu.Lock()
		c.obsSnap = observe.Snapshot{Services: map[string]observe.ServiceMetrics{
			"discord": {Service: "discord", Flows: flows, Bytes: bytes, StalledRatio: stalled},
		}}
		c.obsMu.Unlock()
	}

	// First sighting has nothing to compare against: a cumulative total is not
	// evidence that anything moved.
	set(5<<20, 4, 0)
	if _, ok := c.CarryingReason("discord"); ok {
		t.Error("the first observation must not count as movement")
	}

	// A flow that exists but whose counter stopped is indistinguishable from a
	// busy one in a single snapshot — and it is exactly what a frozen path looks
	// like.
	set(5<<20, 4, 0)
	if _, ok := c.CarryingReason("discord"); ok {
		t.Error("an unchanged byte total is not movement")
	}

	// Real movement.
	set(5<<20+(256<<10), 4, 0)
	reason, ok := c.CarryingReason("discord")
	if !ok {
		t.Fatal("256 KiB across live flows must count as carrying")
	}
	if !strings.Contains(reason, "KiB") {
		t.Errorf("the reason must carry the evidence, got %q", reason)
	}

	// Movement, but most flows frozen: that is the freeze signature, and calling
	// it healthy because the rest still moves is the false green this prevents.
	set(6<<20, 4, 0.9)
	if _, ok := c.CarryingReason("discord"); ok {
		t.Error("a mostly-frozen rule must not count as carrying")
	}

	// The defect this floor exists for: a browser thrashing. 1110 KiB looks like
	// plenty until you divide it by 387 connections and get 2.9 KiB each — which is
	// the TSPU freeze, not health. Summing them turns the symptom into evidence.
	// The per-flow floor and the freeze signature now live with the judgement, in
	// pkg/observe — see TestCarryingDividesByFlowsBeforeBelievingTheTotal. What
	// belongs here is that the CLIENT reads its own snapshot and delegates.

	// Nothing observed at all.
	if _, ok := c.CarryingReason("nosuch"); ok {
		t.Error("an unobserved rule cannot be carrying")
	}
}

// A verdict may only be filed by a measurement that HAPPENED. The distinction did
// not exist while the canary ran once, at apply time — declining and passing both
// returned a bare true, and nothing downstream could tell them apart. It matters
// the moment anything runs periodically: a sweep that could not measure would
// otherwise clear a real verdict taken minutes earlier, and the escalation that
// verdict had earned would disappear with no line saying so.
func TestGoodputSeparatesDecliningFromPassing(t *testing.T) {
	body := strings.Repeat("x", 256<<10)
	full := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer full.Close()
	// A 204 with no body — youtube's real probe target, and the endpoint that once
	// taught the knowledge base that every recipe for it scored zero.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer empty.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	c := &Core{log: slog.New(slog.DiscardHandler)}
	c.opts.CanaryGoodputBytes = 64 << 10

	c.opts.CanaryGoodputKBps = 0
	if ok, _, measured := c.goodputOK(context.Background(), registry.Service{Name: "youtube", VolumeTarget: full.URL}); !ok || measured {
		t.Error("with the check disabled nothing was measured, and it must not claim otherwise")
	}

	c.opts.CanaryGoodputKBps = 8
	if ok, _, measured := c.goodputOK(context.Background(), registry.Service{Name: "youtube"}); !ok || measured {
		t.Error("a rule with no volume target measures nothing")
	}
	if ok, _, measured := c.goodputOK(context.Background(), registry.Service{Name: "youtube", ProbeTarget: empty.URL}); !ok || measured {
		t.Error("an endpoint with less to give than we ask for describes the URL, not the path")
	}
	if ok, _, measured := c.goodputOK(context.Background(), registry.Service{Name: "youtube", VolumeTarget: full.URL}); !ok || !measured {
		t.Error("256 KiB off a local server is a real measurement and a passing one")
	}

	// Nothing delivered at all. The old single sentence called this "connects but
	// carries nothing", which is precisely what it did not do — and it is the shape
	// of the failure the owner hit: youtube stalling at the TLS handshake.
	ok, why, measured := c.goodputOK(context.Background(), registry.Service{Name: "youtube", VolumeTarget: deadURL})
	if ok || !measured {
		t.Fatalf("a refused fetch is a measurement and a failing one, got ok=%v measured=%v", ok, measured)
	}
	if !strings.Contains(why, "did not complete") {
		t.Errorf("the reason must say the fetch never completed, got %q", why)
	}
}
