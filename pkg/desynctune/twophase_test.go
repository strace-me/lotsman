package desynctune

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/quality"
	"github.com/strace-me/lotsman/pkg/tester"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// The expensive pass must touch only the survivors: measuring throughput for
// every candidate means pulling real volume a hundred-odd times down the
// household's uplink, serialised.
func TestTwoPhaseSpendsBytesOnlyOnSurvivors(t *testing.T) {
	e := zapret.NfqwsEngine{}
	cands := coldCandidates(e, 40)
	if len(cands) < 12 {
		t.Skipf("engine produced %d candidates; too few to exercise the split", len(cands))
	}

	var applied []string
	cheapCalls, burstCalls := 0, 0
	apply := func(_ context.Context, args []string) error {
		applied = append(applied, strings.Join(args, " "))
		return nil
	}
	cheap := func(context.Context) quality.Quality {
		cheapCalls++
		// Everything connects; the ranking is by loss, so give the later arms
		// a slight edge to prove the survivors are CHOSEN, not just the first N.
		return quality.Quality{Samples: 5, Loss: 1 / float64(cheapCalls+1)}
	}
	burst := func(context.Context) quality.Quality {
		burstCalls++
		return quality.Quality{Samples: 5, Loss: 0.01, GoodputKBps: 500}
	}

	keep := 5
	_, err := TwoPhase(context.Background(), e, cands, apply, cheap, burst, 0, keep, tester.ThroughputConfig())
	if err != nil {
		t.Fatal(err)
	}
	// +1 on each side for the no-desync baseline arm.
	if cheapCalls <= keep+1 {
		t.Errorf("cheap pass ran %d times; it must cover every candidate", cheapCalls)
	}
	if burstCalls > keep+1 {
		t.Errorf("burst pass ran %d times, want at most %d — it is meant to be the narrow one", burstCalls, keep+1)
	}
	if len(applied) == 0 {
		t.Error("nothing was ever applied")
	}
}

// Reporting a winner from a phase where nothing connected is how an unverified
// strategy reaches production.
func TestNoSurvivorsIsAnHonestUnviable(t *testing.T) {
	e := zapret.NfqwsEngine{}
	dead := func(context.Context) quality.Quality { return quality.Quality{Samples: 5, Loss: 1} }
	burstCalls := 0
	burst := func(context.Context) quality.Quality { burstCalls++; return quality.Quality{Samples: 5} }

	res, err := TwoPhase(context.Background(), e, coldCandidates(e, 8),
		func(context.Context, []string) error { return nil }, dead, burst, 0, 4, tester.ThroughputConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.Viable() {
		t.Error("a search where nothing connected produced a viable verdict")
	}
	if burstCalls != 0 {
		t.Errorf("the expensive pass ran %d times despite no survivors", burstCalls)
	}
	if !strings.Contains(res.Verdict.Reason, "prescreen") {
		t.Errorf("the verdict does not say why: %q", res.Verdict.Reason)
	}
}

// Serialisation is a correctness requirement, not an optimisation: parallel
// handshakes to one SNI provoke the very freeze being measured.
func TestArmsAreProbedOneAtATime(t *testing.T) {
	e := zapret.NfqwsEngine{}
	inFlight, maxInFlight := 0, 0
	probe := func(context.Context) quality.Quality {
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		time.Sleep(time.Millisecond)
		inFlight--
		return quality.Quality{Samples: 5, Loss: 0.05, GoodputKBps: 500}
	}
	_, _ = TwoPhase(context.Background(), e, coldCandidates(e, 6),
		func(context.Context, []string) error { return nil }, probe, probe, 0, 3, tester.DefaultConfig())
	if maxInFlight > 1 {
		t.Errorf("%d probes overlapped; trials must be strictly sequential", maxInFlight)
	}
}
