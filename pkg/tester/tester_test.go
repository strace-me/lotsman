package tester

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/quality"
)

func q(loss, p95 float64, samples int) quality.Quality {
	return quality.Quality{Loss: loss, P95ms: p95, Samples: samples}
}

func TestNotBlockedNeedsNoDesync(t *testing.T) {
	// Direct works -> no recipe needed, even if a recipe also looks fine.
	v := Decide(q(0.0, 120, 10), []Trial{{"zms-dv7", q(0.0, 90, 10)}}, DefaultConfig())
	if v.Outcome != OutcomeNoDesync || !v.Viable() {
		t.Fatalf("outcome = %q (viable=%v), want no_desync; reason=%s", v.Outcome, v.Viable(), v.Reason)
	}
}

func TestRecipeWinsWhenItBeatsBaseline(t *testing.T) {
	// Direct is blocked (100% loss); a recipe restores it (0% loss) -> use it.
	trials := []Trial{
		{"bad", q(0.9, 100, 10)},     // still mostly failing
		{"good", q(0.0, 150, 10)},    // fixes it
		{"alsogood", q(0.0, 80, 10)}, // fixes it AND lower tail -> should win on p95
	}
	v := Decide(q(1.0, 0, 10), trials, DefaultConfig())
	if v.Outcome != OutcomeRecipe {
		t.Fatalf("outcome = %q, want recipe; reason=%s", v.Outcome, v.Reason)
	}
	if v.RecipeID != "alsogood" {
		t.Errorf("chosen = %q, want alsogood (lowest loss, then lowest p95)", v.RecipeID)
	}
}

func TestNoRecipeHelpsIsUnviable(t *testing.T) {
	// Blocked, and every recipe is still unreliable -> rung unviable (Brain will
	// escalate; the tester does NOT decide VPN).
	trials := []Trial{
		{"a", q(0.6, 200, 10)},
		{"b", q(0.5, 200, 10)}, // beats baseline a bit but still >MaxRecipeLoss
	}
	v := Decide(q(1.0, 0, 10), trials, DefaultConfig())
	if v.Outcome != OutcomeUnviable || v.Viable() {
		t.Fatalf("outcome = %q (viable=%v), want unviable; reason=%s", v.Outcome, v.Viable(), v.Reason)
	}
}

func TestMarginGuardsAgainstNoise(t *testing.T) {
	// Baseline just over the not-blocked line (loss 0.12); a recipe at loss 0.0
	// beats it by 0.12 — LESS than the 0.20 margin — so it is NOT credited.
	v := Decide(q(0.12, 100, 20), []Trial{{"marginal", q(0.0, 100, 20)}}, DefaultConfig())
	if v.Outcome != OutcomeUnviable {
		t.Fatalf("outcome = %q, want unviable (recipe within noise margin not credited); reason=%s", v.Outcome, v.Reason)
	}
}

func TestUntrustedSamplesIgnored(t *testing.T) {
	// A recipe with too few samples must not be chosen even if it looks perfect.
	v := Decide(q(1.0, 0, 10), []Trial{{"thin", q(0.0, 50, 2)}}, DefaultConfig())
	if v.Outcome != OutcomeUnviable {
		t.Fatalf("outcome = %q, want unviable (thin sample ignored); reason=%s", v.Outcome, v.Reason)
	}
}
