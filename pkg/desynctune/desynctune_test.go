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

func goodWhen(cur *string, want string) tester.Probe {
	return func(_ context.Context) quality.Quality {
		if *cur == want {
			return good()
		}
		return bad()
	}
}

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

func TestCandidatesSeedMutatesElseGrids(t *testing.T) {
	e := zapret.NfqwsEngine{}
	// With a seed -> Mutate (neighbours of the seed; bounded, not the full grid).
	seed := desyncgen.Strategy{"method": "multisplit", "split_pos": "2"}
	mut := Candidates(e, seed, 1000)
	grid := Candidates(e, nil, 1000)
	if len(mut) == 0 {
		t.Fatal("seed should produce mutation candidates")
	}
	if len(grid) == 0 {
		t.Fatal("no seed should produce grid candidates")
	}
	if len(mut) >= len(grid) {
		t.Errorf("mutation (%d) should be smaller than the cold grid (%d)", len(mut), len(grid))
	}
}

// A seeded run is multi-start: it must include points that change 2+ axes from
// the seed (escaping its basin / crossing a conjunctive valley), not just the
// radius-1 neighbourhood the old Candidates produced.
func TestCandidatesMultiStartEscapesSeedBasin(t *testing.T) {
	e := zapret.NfqwsEngine{}
	seed := desyncgen.Strategy{"method": "multisplit", "split_pos": "2", "seqovl": "0", "ttl": "0", "fooling": "none", "fake_tls": "none", "repeats": "1"}
	axes := []string{"method", "split_pos", "seqovl", "ttl", "fooling", "fake_tls", "repeats"}
	far := 0
	for _, s := range Candidates(e, seed, 1000) {
		diff := 0
		for _, a := range axes {
			if s[a] != seed[a] {
				diff++
			}
		}
		if diff >= 2 {
			far++
		}
	}
	if far == 0 {
		t.Error("multi-start seeded candidates should include points 2+ axes from the seed")
	}
}

// A cold (no-seed) run must seed from the engine's prior catalog, not a blind
// Grid — the human recipes are the priors the sweep can't rediscover. Assert a
// documented recipe is present in the cold candidate set.
func TestColdCandidatesIncludePriors(t *testing.T) {
	e := zapret.NfqwsEngine{}
	want := strings.Join(e.Render(e.Seeds()[1]), " ") // the ALT3 fake-SNI-decoy prior
	found := false
	for _, s := range Candidates(e, nil, 1000) {
		if strings.Join(e.Render(s), " ") == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cold candidates missing prior recipe %q", want)
	}
}

func TestCandidatesGridRespectsCap(t *testing.T) {
	e := zapret.NfqwsEngine{}
	if got := Candidates(e, nil, 5); len(got) > 5 {
		t.Errorf("grid cap not respected: %d > 5", len(got))
	}
}

func TestTuneServiceReturnsWinnerArgs(t *testing.T) {
	e := zapret.NfqwsEngine{}
	winner := desyncgen.Strategy{"method": "multisplit", "split_pos": "2"}
	winArgs := strings.Join(e.Render(winner), " ")
	cur := ""
	apply := func(_ context.Context, args []string) error { cur = strings.Join(args, " "); return nil }
	probe := goodWhen(&cur, winArgs) // good only when the winner is applied
	res, err := TuneService(context.Background(), e, winner, 1000, apply, probe, 0, tester.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Viable() {
		t.Fatalf("expected a viable winner, verdict=%v", res.Verdict.Outcome)
	}
	if strings.Join(res.Args, " ") != winArgs {
		t.Errorf("winner args = %q, want %q", strings.Join(res.Args, " "), winArgs)
	}
}

// End-to-end: the WHOLE real pipeline (cold Candidates seeded from the engine's
// priors -> Tune A/B -> throughput-aware Decide) must SELECT a planted optimum.
// Baseline (no desync) and every wrong recipe "freeze" (collapsed goodput); only
// the planted winner sustains throughput. No data plane, no real traffic — the
// apply just records the current args and the probe is a synthetic fitness oracle.
// This is the safe proof that the search now finds a hand-recipe-class point.
func TestTuneEndToEndSelectsPlantedOptimum(t *testing.T) {
	e := zapret.NfqwsEngine{}
	winner := e.Seeds()[1] // the ALT3 fake-SNI-decoy prior — guaranteed in the cold set
	winArgs := strings.Join(e.Render(winner), " ")

	var current string
	apply := func(_ context.Context, args []string) error {
		current = strings.Join(args, " ") // "" for the no-desync baseline
		return nil
	}
	probe := func(_ context.Context) quality.Quality {
		if current == winArgs {
			return quality.FromBurst([]float64{30, 30, 30, 30, 30}, 5, 64<<10, 400) // restored
		}
		return quality.FromBurst([]float64{30, 30, 30, 30, 30}, 5, 1<<10, 2) // frozen: low goodput
	}

	cands := Candidates(e, nil, 300) // cold start from priors
	w, v, err := Tune(context.Background(), e, cands, apply, probe, 0, tester.ThroughputConfig())
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != tester.OutcomeRecipe {
		t.Fatalf("pipeline should select a recipe, got %s (%s)", v.Outcome, v.Reason)
	}
	if got := strings.Join(e.Render(w), " "); got != winArgs {
		t.Errorf("selected %q, want the planted optimum %q", got, winArgs)
	}
}
