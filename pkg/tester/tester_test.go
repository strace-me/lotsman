package tester

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/quality"
)

func q(loss, p95 float64, samples int) quality.Quality {
	return quality.Quality{Loss: loss, P95ms: p95, Samples: samples}
}

func qg(loss float64, samples int, goodput float64) quality.Quality {
	return quality.Quality{Loss: loss, Samples: samples, GoodputKBps: goodput}
}

// The TSPU IP-volume-freeze: baseline connects with LOW loss but goodput
// collapses (~1 KiB/s). Loss-only Decide would call it NoDesync ("not blocked");
// the throughput gate must NOT — and when no recipe sustains goodput either
// (desync can't beat an IP-freeze), the rung is Unviable so Brain escalates.
func TestThroughputFreezeIsNotHealthyAndUnviable(t *testing.T) {
	cfg := ThroughputConfig()
	frozen := qg(0.0, 10, 1.0) // low loss, dead goodput
	v := Decide(frozen, []Trial{{"any-recipe", qg(0.0, 10, 2.0)}}, cfg)
	if v.Outcome != OutcomeUnviable {
		t.Fatalf("frozen path must be Unviable under throughput gate, got %s (%s)", v.Outcome, v.Reason)
	}
	// Sanity: loss-only Decide is fooled into NoDesync (the old blind spot).
	if old := Decide(frozen, nil, DefaultConfig()); old.Outcome != OutcomeNoDesync {
		t.Fatalf("precondition: loss-only should mis-call frozen as NoDesync, got %s", old.Outcome)
	}
}

// A recipe that restores sustained goodput (above floor + margin) wins even
// though loss didn't move — goodput is the objective in throughput mode.
func TestThroughputRecipeWinsOnGoodputGain(t *testing.T) {
	cfg := ThroughputConfig()
	baseline := qg(0.0, 10, 5.0) // connects, but throttled
	trials := []Trial{
		{"still-throttled", qg(0.0, 10, 8.0)},  // below floor → rejected
		{"restores-speed", qg(0.0, 10, 400.0)}, // well above floor + margin → wins
	}
	v := Decide(baseline, trials, cfg)
	if v.Outcome != OutcomeRecipe || v.RecipeID != "restores-speed" {
		t.Fatalf("want Recipe restores-speed, got %s %q (%s)", v.Outcome, v.RecipeID, v.Reason)
	}
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

// A winner that fixes its own service but spikes a bystander's loss must be
// vetoed (the discord regression). A scoped, harmless recipe passes.
func TestGuardVetoesBystanderRegression(t *testing.T) {
	cfg := DefaultConfig()
	guards := []Guard{
		{"youtube", q(0.5, 100, 10), q(0.0, 90, 10)}, // the target: improved, fine
		{"discord", q(0.0, 80, 10), q(0.6, 300, 10)}, // bystander: loss 0 -> 0.6 = regressed
	}
	if svc, ok := GuardOK(guards, cfg); ok || svc != "discord" {
		t.Fatalf("must veto on discord regression, got svc=%q ok=%v", svc, ok)
	}

	safe := []Guard{
		{"discord", q(0.0, 80, 10), q(0.05, 85, 10)}, // tiny wobble within margin
		{"battlenet", q(0.1, 200, 10), q(0.1, 210, 10)},
	}
	if svc, ok := GuardOK(safe, cfg); !ok {
		t.Fatalf("scoped recipe should pass guard, got veto on %q", svc)
	}
}

// In throughput mode a bystander whose goodput collapses is a regression even if
// its loss is unchanged.
func TestGuardCatchesGoodputRegression(t *testing.T) {
	cfg := ThroughputConfig()
	guards := []Guard{{"video", qg(0.0, 10, 400), qg(0.0, 10, 5)}} // 400 -> 5 KiB/s
	if _, ok := GuardOK(guards, cfg); ok {
		t.Fatal("goodput collapse must be caught as a regression")
	}
}
