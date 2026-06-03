package tuner

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/quality"
)

func q(loss, p95 float64) quality.Quality {
	return quality.Quality{Loss: loss, P95ms: p95, Samples: 5}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"": ModeOff, "off": ModeOff, "false": ModeOff, "no": ModeOff,
		"on": ModeOn, "true": ModeOn, "yes": ModeOn, "1": ModeOn,
		"auto": ModeAuto, "AUTO": ModeAuto, "weird": ModeOff,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResolveTurnsOnWhenVariantClearlyBetter(t *testing.T) {
	tn := New(0.1)
	// variant fixes loss (fragment helps a blocked SNI get through).
	on, changed := tn.Resolve("youtube/frag", q(0.8, 100), q(0.0, 120))
	if !on || !changed {
		t.Fatalf("variant clearly better -> on/changed, got on=%v changed=%v", on, changed)
	}
}

func TestResolveStaysOffOnNoise(t *testing.T) {
	tn := New(0.1)
	// near-identical -> no clear winner -> stay off (first sight).
	on, _ := tn.Resolve("k", q(0.0, 100), q(0.0, 98))
	if on {
		t.Error("marginal difference must not turn the knob on")
	}
}

func TestResolveHysteresisHoldsState(t *testing.T) {
	tn := New(0.1)
	// Turn on with a clear win.
	if on, _ := tn.Resolve("k", q(0.5, 100), q(0.0, 90)); !on {
		t.Fatal("should turn on")
	}
	// Next cycle: variant only marginally better/worse -> stays ON (no flap).
	on, changed := tn.Resolve("k", q(0.0, 100), q(0.0, 103))
	if !on || changed {
		t.Errorf("marginal change must hold ON (no flap), got on=%v changed=%v", on, changed)
	}
}

func TestResolveTurnsOffWhenVariantClearlyWorse(t *testing.T) {
	tn := New(0.1)
	tn.Resolve("k", q(0.5, 100), q(0.0, 90)) // on
	// Now the variant clearly hurts (adds loss) -> turn off.
	on, changed := tn.Resolve("k", q(0.0, 100), q(0.6, 100))
	if on || !changed {
		t.Errorf("variant clearly worse -> off/changed, got on=%v changed=%v", on, changed)
	}
}

func TestBetterLossDominates(t *testing.T) {
	// Lower loss wins even with worse latency.
	if !better(q(0.0, 300), q(0.5, 50), 0.1) {
		t.Error("clearly lower loss must beat better latency")
	}
}
