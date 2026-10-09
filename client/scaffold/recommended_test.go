package scaffold

import (
	"path/filepath"
	"testing"
)

// TestRecommendedLoads guards the shipped default. It must load BOTH without a
// subscription — the first-run state, where the user adds one later — and with one,
// because those are the two states `-init -recommended` is reachable from and a
// scaffold that only works in one of them is broken onboarding rather than a rough
// edge.
func TestRecommendedLoads(t *testing.T) {
	if _, err := write(t, Recommended(nil)); err != nil {
		t.Fatalf("the recommended deployment (no subscription) does not load: %v", err)
	}
	if _, err := write(t, Recommended([]string{"https://example.com/sub"})); err != nil {
		t.Fatalf("the recommended deployment (+subscription) does not load: %v", err)
	}
}

func TestRecommendedSplitsIntoOneFilePerRule(t *testing.T) {
	// One file per rule is the whole reason a rule can be edited without reformatting
	// the others. A multi-document blob that never got split would pass every load and
	// leave the operator editing a file with five rules in it.
	dep := Recommended(nil)
	if len(dep.Rules) < 5 {
		t.Fatalf("rules = %d, want the curated set split one per file", len(dep.Rules))
	}
	for name := range dep.Rules {
		if filepath.Ext(name) != ".yaml" {
			t.Errorf("rule file %q is not a .yaml name", name)
		}
	}
}

func TestRecommendedGivesAIDesyncNoRungOnPurpose(t *testing.T) {
	// An application-layer refusal is not a DPI handshake problem. A desync rung there
	// costs a restart and buys nothing — and the rule would sit on it reporting healthy
	// while the service stayed blocked.
	cfg, err := write(t, Recommended(nil))
	if err != nil {
		t.Fatal(err)
	}
	ai, ok := cfg.Registry.Services["ai"]
	if !ok {
		t.Fatal("the recommended set has no ai rule")
	}
	for _, step := range ai.Chain {
		if step.StrategyClass == "zapret" {
			t.Fatalf("the ai rule has a desync rung: %v", ai.Chain)
		}
	}
}
