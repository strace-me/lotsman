package noderank

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/balancer"
)

func canaryRanker(t *testing.T, api *fakeAPI) *Ranker {
	t.Helper()
	r := New(api, 1, true, quietLog())
	r.SwitchMargin = -1 // take the top node; stickiness is tested elsewhere
	return r
}

// The property the whole spec is written for: an exit that answers quickly and
// carries nothing must not be chosen. Latency crowns it; the measurement must
// take it away.
func TestAnExitThatAnswersAndCarriesNothingLosesToASlowerOneThatCarries(t *testing.T) {
	api := newFake("", []string{"vpn_url_test", "frozen", "slow-but-real", "direct"},
		map[string]map[string]int{
			"frozen":        {defaultScreenURL: 40, svcURL: 40},
			"slow-but-real": {defaultScreenURL: 300, svcURL: 300},
		})
	r := canaryRanker(t, api)
	r.CanaryFloorKBps = 64
	r.Canary = func(_ context.Context, _, node string) (float64, bool) {
		if node == "frozen" {
			return 0.5, true // answers in 40ms and moves nothing: the TSPU volume freeze
		}
		return 900, true
	}

	// Two passes: one node is measured per pass, by design.
	var got string
	for i := 0; i < 2; i++ {
		var err error
		got, err = r.Pick(context.Background(), Service{
			Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL,
			Weights: balancer.ProfileFor("streaming"),
		}, []Candidate{{Tag: "frozen"}, {Tag: "slow-but-real"}})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
	}
	if got != "slow-but-real" {
		t.Errorf("advised %q — the fastest exit carries nothing and latency still won", got)
	}
}

// One node per service per pass, serialised. A fan-out here would provoke the
// very freeze the canary measures for: more than three parallel TLS handshakes.
func TestOnlyOneExitIsMeasuredPerPass(t *testing.T) {
	api := newFake("", []string{"vpn_url_test", "a", "b", "c", "direct"},
		map[string]map[string]int{
			"a": {defaultScreenURL: 40, svcURL: 40},
			"b": {defaultScreenURL: 50, svcURL: 50},
			"c": {defaultScreenURL: 60, svcURL: 60},
		})
	r := canaryRanker(t, api)

	var mu sync.Mutex
	var calls int
	var concurrent, peak int
	r.Canary = func(_ context.Context, _, _ string) (float64, bool) {
		mu.Lock()
		calls++
		concurrent++
		if concurrent > peak {
			peak = concurrent
		}
		mu.Unlock()
		defer func() { mu.Lock(); concurrent--; mu.Unlock() }()
		return 500, true
	}

	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{Tag: "a"}, {Tag: "b"}, {Tag: "c"}}
	if _, err := r.Pick(context.Background(), svc, cands); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if calls != 1 {
		t.Errorf("one pass measured %d exits, want exactly 1", calls)
	}
	if peak > 1 {
		t.Errorf("peak concurrency %d — measurements must be serialised", peak)
	}

	// Three passes reach all three, rather than re-measuring the first.
	for i := 0; i < 2; i++ {
		if _, err := r.Pick(context.Background(), svc, cands); err != nil {
			t.Fatalf("Pick: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("after three passes %d exits were measured, want 3 — attention must spread", calls)
	}
	if _, err := r.Pick(context.Background(), svc, cands); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if calls != 3 {
		t.Errorf("a fourth pass re-measured a fresh exit (%d calls) — freshness is not honoured", calls)
	}
}

// A refusal is not a zero. The hook declines while a live session is on the link,
// or when the rule has no volume target; recording that as "carries nothing" would
// demote a good exit for being busy.
func TestADeclinedCanaryIsNotRecordedAsCarryingNothing(t *testing.T) {
	api := newFake("", []string{"vpn_url_test", "a", "direct"},
		map[string]map[string]int{"a": {defaultScreenURL: 40, svcURL: 40}})
	r := canaryRanker(t, api)
	r.CanaryFloorKBps = 64
	r.Canary = func(context.Context, string, string) (float64, bool) { return 0, false }

	got, err := r.Pick(context.Background(), Service{
		Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general"),
	}, []Candidate{{Tag: "a"}})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got != "a" {
		t.Errorf("advised %q — a declined measurement demoted the only exit", got)
	}
}

// The floor must act on CANARY samples only. MinGoodputKBps applies to the passive
// hook, which measures demand: an idle service carries little through a perfectly
// good exit, and demoting on that is LOT-52 all over again.
func TestTheCanaryFloorIsSeparateFromThePassiveOne(t *testing.T) {
	r := New(&fakeAPI{}, 1, true, quietLog())
	r.Canary = func(context.Context, string, string) (float64, bool) { return 0, false }
	r.CanaryFloorKBps = 64
	r.canary = map[string]canarySample{"measured-frozen": {kbps: 1, at: time.Now()}}

	if got, decided := r.promote("youtube", "measured-frozen", "", []string{"measured-frozen"}); decided {
		t.Errorf("advised %q with no alternative — demotion must not invent a node", got)
	}
	// And an exit with only a PASSIVE reading is untouched by the canary floor.
	if _, decided := r.promote("youtube", "never-canaried", "", []string{"never-canaried"}); decided {
		t.Error("an exit the canary never measured was judged by the canary floor")
	}
}

// Promotion needs BOTH sides measured. One measured challenger against an
// unmeasured incumbent is not a comparison — and where there is no evidence the
// answer is to hold, never to hand the decision back to latency.
func TestPromotionRequiresBothSidesMeasured(t *testing.T) {
	r := New(&fakeAPI{}, 1, true, quietLog())
	r.Canary = func(context.Context, string, string) (float64, bool) { return 0, false }
	now := time.Now()
	r.canary = map[string]canarySample{"challenger": {kbps: 900, at: now}}

	if _, decided := r.promote("youtube", "challenger", "incumbent", []string{"challenger", "incumbent"}); decided {
		t.Error("promoted against an incumbent nobody measured")
	}

	// With both measured and the margin cleared, it moves.
	r.canary["incumbent"] = canarySample{kbps: 100, at: now}
	got, decided := r.promote("youtube", "challenger", "incumbent", []string{"challenger", "incumbent"})
	if !decided || got != "challenger" {
		t.Errorf("got %q decided=%v — a challenger carrying 9x more did not win", got, decided)
	}

	// Within the margin, the incumbent keeps its place: a switch costs a session.
	r.canary["incumbent"] = canarySample{kbps: 800, at: now}
	if got, _ := r.promote("youtube", "challenger", "incumbent", []string{"challenger", "incumbent"}); got != "incumbent" {
		t.Errorf("got %q — a 12%% difference churned a session", got)
	}
}

// A result is evidence about a moment. The freeze is per-connection and
// time-varying, so a stale sample must stop counting rather than stand in for one.
func TestAStaleMeasurementStopsCounting(t *testing.T) {
	r := New(&fakeAPI{}, 1, true, quietLog())
	r.CanaryEvery = time.Minute
	r.canary = map[string]canarySample{"a": {kbps: 900, at: time.Now().Add(-2 * time.Minute)}}

	if _, ok := r.measuredCarry("a", time.Now()); ok {
		t.Error("a two-minute-old sample was served under a one-minute freshness window")
	}
	if due := r.dueForCanary([]string{"a"}, time.Now()); due != "a" {
		t.Errorf("due = %q, want the stale node re-measured", due)
	}
}
