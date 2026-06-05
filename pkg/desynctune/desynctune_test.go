package desynctune

import (
	"context"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/desyncgen"
	"github.com/strace-me/lotsman/pkg/quality"
	"github.com/strace-me/lotsman/pkg/tester"
	"github.com/strace-me/lotsman/pkg/zapret"
)

func good() quality.Quality { return quality.FromRTTs([]float64{20, 20, 20, 20, 20}, 5) } // loss 0
func bad() quality.Quality  { return quality.FromRTTs(nil, 5) }                           // loss 1

func TestTunePicksTheWinningStrategy(t *testing.T) {
	e := zapret.NfqwsEngine{}
	// Two candidates that render differently.
	loser := desyncgen.Strategy{"method": "fake"}
	winner := desyncgen.Strategy{"method": "multisplit", "split_pos": "2"}
	winArgs := strings.Join(e.Render(winner), " ")

	// Stateful fakes: apply records the current desync; probe is GOOD only when the
	// winner is applied (baseline + loser stay blocked).
	var cur string
	apply := func(_ context.Context, args []string) error { cur = strings.Join(args, " "); return nil }
	probe := tester.Probe(func(_ context.Context) quality.Quality {
		if cur == winArgs {
			return good()
		}
		return bad()
	})

	best, v, err := Tune(context.Background(), e, []desyncgen.Strategy{loser, winner}, apply, probe, 0, tester.DefaultConfig())
	if err != nil {
		t.Fatalf("tune: %v", err)
	}
	if v.Outcome != tester.OutcomeRecipe {
		t.Fatalf("verdict = %v, want recipe", v.Outcome)
	}
	if best == nil || best["method"] != "multisplit" {
		t.Errorf("winner = %v, want the multisplit strategy", best)
	}
	if v.RecipeID != StrategyID(e.Render(winner)) {
		t.Errorf("verdict id = %q, want %q", v.RecipeID, StrategyID(e.Render(winner)))
	}
}

func TestTuneNoViableRecipeReturnsNil(t *testing.T) {
	e := zapret.NfqwsEngine{}
	// Everything stays blocked -> Unviable -> nil winner.
	apply := func(_ context.Context, _ []string) error { return nil }
	probe := tester.Probe(func(_ context.Context) quality.Quality { return bad() })

	best, v, err := Tune(context.Background(), e, []desyncgen.Strategy{{"method": "fake"}, {"method": "split2"}}, apply, probe, 0, tester.DefaultConfig())
	if err != nil {
		t.Fatalf("tune: %v", err)
	}
	if v.Viable() {
		t.Errorf("verdict should be unviable, got %v", v.Outcome)
	}
	if best != nil {
		t.Errorf("no viable recipe -> nil winner, got %v", best)
	}
}

func TestTuneBaselineHealthyIsNoDesync(t *testing.T) {
	e := zapret.NfqwsEngine{}
	// Baseline already works (no desync) -> NoDesync, no winner needed.
	apply := func(_ context.Context, _ []string) error { return nil }
	probe := tester.Probe(func(_ context.Context) quality.Quality { return good() })

	best, v, err := Tune(context.Background(), e, []desyncgen.Strategy{{"method": "fake"}}, apply, probe, 0, tester.DefaultConfig())
	if err != nil {
		t.Fatalf("tune: %v", err)
	}
	if v.Outcome != tester.OutcomeNoDesync {
		t.Errorf("healthy baseline -> no_desync, got %v", v.Outcome)
	}
	if best != nil {
		t.Errorf("no_desync -> nil winner, got %v", best)
	}
}

func TestStrategyIDStable(t *testing.T) {
	a := StrategyID([]string{"--dpi-desync=multisplit", "--dpi-desync-split-pos=2"})
	b := StrategyID([]string{"--dpi-desync=multisplit", "--dpi-desync-split-pos=2"})
	c := StrategyID([]string{"--dpi-desync=fake"})
	if a != b {
		t.Error("same args must yield same id")
	}
	if a == c {
		t.Error("different args must yield different ids")
	}
	if !strings.HasPrefix(a, "gen-") {
		t.Errorf("id should be gen-prefixed, got %q", a)
	}
}
