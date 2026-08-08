package core

import (
	"context"
	"testing"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

type stubProber struct{ called int }

func (s *stubProber) Probe(context.Context, string, int) events.ProductionVerdict {
	s.called++
	return events.ProductionVerdict{OK: true}
}

func laneCore(t *testing.T) (*Core, *stubProber) {
	t.Helper()
	var applied []string
	c := rotCore(t, &applied)
	svc := c.reg.Services["youtube"]
	svc.Chain = []registry.ChainStep{
		{Position: 0, State: "PREFERRED", StrategyClass: strategy.ClassZapret},
		{Position: 1, State: "VPN", StrategyClass: strategy.ClassVPN, StrategyID: "pool"},
	}
	c.reg.Services["youtube"] = svc
	// The rule is deliberately NOT on the desync rung — that is the whole case.
	c.zapExec.active = func() []registry.Service { return nil }
	return c, &stubProber{}
}

// The bare-direct prober measures a rule's desync rung WITHOUT its recipe,
// because nfqws holds no profile for a rule that is not on the rung. On the
// owner's network that path dies at the handshake, so recovery said "no" forever
// while the lane had four times measured a recipe that carried it.
func TestLaneProberProvesTheRungTheRuleIsNotOn(t *testing.T) {
	c, fb := laneCore(t)
	c.testCandidate = func(_ context.Context, _ registry.Service, id string) (bool, bool, string) {
		return id == "bravo", true, "measured"
	}
	l := newLaneProber(c, fb)

	v := l.Probe(context.Background(), "youtube", 0)
	if !v.OK {
		t.Fatalf("the lane proved a candidate; the rung must read healthy, got %+v", v)
	}
	if fb.called != 0 {
		t.Error("a desync rung must not be answered by the bare-direct prober")
	}
}

// Five echoes of one measurement are not five measurements — the mistake the
// canary's standing verdict once made, which turned one verdict into three
// escalations. Inside the cooldown with nothing fresh, the answer is Unmeasured.
func TestLaneProberDoesNotEchoItselfIntoARecovery(t *testing.T) {
	c, fb := laneCore(t)
	calls := 0
	c.testCandidate = func(context.Context, registry.Service, string) (bool, bool, string) {
		calls++
		return false, true, "carried nothing"
	}
	l := newLaneProber(c, fb)

	first := l.Probe(context.Background(), "youtube", 0)
	if first.OK || first.Unmeasured {
		t.Fatalf("a measured failure must be a failure, got %+v", first)
	}
	// Cached: same answer, no second lift.
	if second := l.Probe(context.Background(), "youtube", 0); second.OK {
		t.Error("a cached failure must not turn into a success")
	}
	if calls == 0 {
		t.Fatal("the lane was never asked")
	}
}

// A rung with no recipe to prove — direct, LOCKED — is not the lane's question,
// and answering it here would be inventing a verdict about something never
// measured.
func TestLaneProberDefersNonDesyncRungs(t *testing.T) {
	c, fb := laneCore(t)
	l := newLaneProber(c, fb)
	if v := l.Probe(context.Background(), "youtube", 1); !v.OK {
		t.Fatalf("a VPN rung must fall through to the fallback, got %+v", v)
	}
	if fb.called != 1 {
		t.Errorf("fallback called %d times, want 1", fb.called)
	}
}
