package zaptune

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

func TestPickerPrefersConsensusOnlyWhenLearningIsSilent(t *testing.T) {
	svc := registry.Service{Name: "youtube"}
	cands := []strategycat.Recipe{
		{ID: "a", Consensus: 1},
		{ID: "b", Consensus: 19}, // shipped by nineteen bundles
		{ID: "c", Consensus: 3},
	}

	// Cold KB: every score equal, so the shared prior decides.
	cold := KBPicker(func(string, string) float64 { return 0.5 })
	if got, _ := cold(svc, cands); got.ID != "b" {
		t.Errorf("cold pick = %q, want the most widely shipped recipe", got.ID)
	}

	// A measured outcome must outrank agreement — consensus is a guess, an
	// outcome is evidence.
	warm := KBPicker(func(_, id string) float64 {
		if id == "a" {
			return 0.9
		}
		return 0.5
	})
	if got, _ := warm(svc, cands); got.ID != "a" {
		t.Errorf("warm pick = %q, want the recipe that actually worked", got.ID)
	}
}
