package core

import (
	"context"
	"log/slog"
	"testing"

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
		canary: func(context.Context, string) bool { return probeOK },
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
