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
	// Cached: no second lift, and no second verdict either.
	if second := l.Probe(context.Background(), "youtube", 0); second.OK || !second.Unmeasured {
		t.Errorf("a cached answer must be reported as Unmeasured, got %+v", second)
	}
	if calls == 0 {
		t.Fatal("the lane was never asked")
	}
}

// The same rule in the other direction: a PASS must be reported once and then
// stop counting. The brain recovers on five CONSECUTIVE successes, so a cached
// yes replayed every ten seconds turns one lift into a recovery — which is the
// standing verdict the whole probe was built to stop repeating.
func TestLaneProberReportsAPassOnlyOnce(t *testing.T) {
	c, fb := laneCore(t)
	c.testCandidate = func(_ context.Context, _ registry.Service, id string) (bool, bool, string) {
		return id == "bravo", true, "measured"
	}
	l := newLaneProber(c, fb)

	if first := l.Probe(context.Background(), "youtube", 0); !first.OK {
		t.Fatalf("the lane proved a candidate, want a success, got %+v", first)
	}
	second := l.Probe(context.Background(), "youtube", 0)
	if second.OK {
		t.Errorf("one measurement must not be reported as two successes, got %+v", second)
	}
	if !second.Unmeasured {
		t.Errorf("a replayed answer is not a verdict; want Unmeasured, got %+v", second)
	}
}

// Discord, live on the owner's laptop: no volume_target, so every candidate
// refused before the lane even lifted — and the probe reported that as a hard NO
// every ten seconds. A failed silent probe RESETS the brain's recovery counter,
// so the rule he wants on desync was pinned to the tunnel by a measurement that
// never happened. Principle 2, from the inside.
func TestLaneProberWillNotFailARungItCouldNotMeasure(t *testing.T) {
	c, fb := laneCore(t)
	c.testCandidate = func(context.Context, registry.Service, string) (bool, bool, string) {
		return false, false, "rule has no volume_target, so the lane has nothing to judge a candidate by"
	}
	l := newLaneProber(c, fb)

	v := l.Probe(context.Background(), "youtube", 0)
	if v.OK {
		t.Fatalf("nothing was measured; a success would be invented, got %+v", v)
	}
	if !v.Unmeasured {
		t.Fatalf("a lane that never asked must say so, not fail the rung: %+v", v)
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
