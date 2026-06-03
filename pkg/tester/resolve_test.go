package tester

import (
	"context"
	"testing"

	"github.com/strace-me/lotsman/pkg/quality"
)

// fakePlane records which arm is applied and serves a scripted quality per arm,
// so Resolve's orchestration is testable without any I/O.
type fakePlane struct {
	current string
	applied []string
	byArm   map[string]quality.Quality
}

func (f *fakePlane) arm(id string) Arm {
	return Arm{ID: id, Apply: func(context.Context) error {
		f.current = id
		f.applied = append(f.applied, id)
		return nil
	}}
}

func (f *fakePlane) probe(context.Context) quality.Quality { return f.byArm[f.current] }

func TestResolvePicksCleanWhenBaselineHealthy(t *testing.T) {
	// Baseline (clean, no desync) is healthy -> NoDesync, recipes need not even
	// be credited. This is the discord-voice case: voice works clean-direct.
	f := &fakePlane{byArm: map[string]quality.Quality{
		"clean":  {Loss: 0.0, P95ms: 30, Samples: 10},
		"recipe": {Loss: 0.0, P95ms: 30, Samples: 10},
	}}
	v, trials, err := Resolve(context.Background(), f.arm("clean"),
		[]Arm{f.arm("recipe")}, f.probe, 0, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != OutcomeNoDesync {
		t.Fatalf("outcome = %q, want no_desync", v.Outcome)
	}
	// it must have measured the baseline (and may measure recipes too).
	if len(f.applied) == 0 || f.applied[0] != "clean" {
		t.Errorf("first applied arm = %v, want clean (baseline measured first)", f.applied)
	}
	if len(trials) != 1 {
		t.Errorf("trials = %d, want 1 (recipe still measured)", len(trials))
	}
}

func TestResolvePicksRecipeWhenItBeatsBlockedBaseline(t *testing.T) {
	// Clean is blocked; a recipe restores it -> Recipe wins.
	f := &fakePlane{byArm: map[string]quality.Quality{
		"clean": {Loss: 1.0, Samples: 10},
		"r-bad": {Loss: 0.8, Samples: 10},
		"r-ok":  {Loss: 0.0, P95ms: 60, Samples: 10},
	}}
	v, _, err := Resolve(context.Background(), f.arm("clean"),
		[]Arm{f.arm("r-bad"), f.arm("r-ok")}, f.probe, 0, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != OutcomeRecipe || v.RecipeID != "r-ok" {
		t.Fatalf("verdict = %+v, want recipe r-ok", v)
	}
}

func TestResolveUnviableWhenNothingWorks(t *testing.T) {
	f := &fakePlane{byArm: map[string]quality.Quality{
		"clean": {Loss: 1.0, Samples: 10},
		"r":     {Loss: 0.9, Samples: 10},
	}}
	v, _, err := Resolve(context.Background(), f.arm("clean"), []Arm{f.arm("r")}, f.probe, 0, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != OutcomeUnviable {
		t.Fatalf("outcome = %q, want unviable (Brain escalates to VPN)", v.Outcome)
	}
}
