package rulesets

import (
	"reflect"
	"sort"
	"testing"
)

func TestPlanSwapPerTag(t *testing.T) {
	required := []string{"geosite-youtube", "geosite-discord", "geosite-ru", "geosite-gone"}
	live := map[string]int{
		"geosite-youtube": 1000,
		"geosite-discord": 200,
		"geosite-ru":      5000,
		"geosite-gone":    50,
	}
	candidate := map[string]int{
		"geosite-youtube": 1100, // grew -> swap
		"geosite-discord": 80,   // halved -> below 0.7 -> keep old
		"geosite-ru":      4900, // -2% -> swap
		// geosite-gone vanished -> keep old
	}
	p := PlanSwap(required, candidate, live, 0.7, true)

	if p.Unusable {
		t.Fatalf("should be usable, got Missing=%v", p.Missing)
	}
	wantSwap := []string{"geosite-ru", "geosite-youtube"}
	if !reflect.DeepEqual(p.Swap, wantSwap) {
		t.Errorf("Swap = %v, want %v", p.Swap, wantSwap)
	}
	if _, ok := p.KeepOld["geosite-discord"]; !ok {
		t.Error("discord halved -> should keep old (per-tag)")
	}
	if info, ok := p.KeepOld["geosite-gone"]; !ok || info.Next != 0 {
		t.Errorf("gone vanished -> keep old with next=0, got %+v ok=%v", info, ok)
	}
}

func TestPlanSwapGlobalRejectsWholeReleaseOnTotalShrink(t *testing.T) {
	required := []string{"a", "b"}
	live := map[string]int{"a": 1000, "b": 1000}
	candidate := map[string]int{"a": 1100, "b": 100} // total 2000 -> 1200 = 60% < 70%
	p := PlanSwap(required, candidate, live, 0.7, false)

	if len(p.Swap) != 0 {
		t.Errorf("global guard should reject all swaps on total shrink, got Swap=%v", p.Swap)
	}
	if _, ok := p.KeepOld["a"]; !ok {
		t.Error("global reject must keep even the grown tag 'a'")
	}
}

func TestPlanSwapGlobalAcceptsWhenTotalHolds(t *testing.T) {
	required := []string{"a", "b"}
	live := map[string]int{"a": 1000, "b": 1000}
	candidate := map[string]int{"a": 1000, "b": 900} // total 1900/2000 = 95% -> ok
	p := PlanSwap(required, candidate, live, 0.7, false)

	sort.Strings(p.Swap)
	if !reflect.DeepEqual(p.Swap, []string{"a", "b"}) {
		t.Errorf("global accept should swap all present, got %v", p.Swap)
	}
}

func TestPlanSwapUnusableWhenRequiredTagAbsentEverywhere(t *testing.T) {
	required := []string{"geosite-youtube", "geosite-missing"}
	live := map[string]int{"geosite-youtube": 1000}
	candidate := map[string]int{"geosite-youtube": 1000} // geosite-missing in neither
	p := PlanSwap(required, candidate, live, 0.7, true)

	if !p.Unusable {
		t.Fatal("a required tag present in neither candidate nor live must be Unusable")
	}
	if len(p.Missing) != 1 || p.Missing[0] != "geosite-missing" {
		t.Errorf("Missing = %v, want [geosite-missing]", p.Missing)
	}
	if len(p.Swap) != 0 {
		t.Errorf("Unusable plan must not swap anything, got %v", p.Swap)
	}
}

func TestPlanSwapFirstRunNoLive(t *testing.T) {
	// Cold start: nothing on disk -> every present tag swaps (prev 0 passes guard).
	required := []string{"a", "b"}
	candidate := map[string]int{"a": 10, "b": 20}
	p := PlanSwap(required, candidate, map[string]int{}, 0.7, true)
	if !reflect.DeepEqual(p.Swap, []string{"a", "b"}) {
		t.Errorf("cold start should swap all, got %v", p.Swap)
	}
}

func TestNextPin(t *testing.T) {
	cases := []struct {
		name            string
		current, latest string
		usable          bool
		wantPin         string
		wantBumped      bool
	}{
		{"newer and valid bumps", "v1", "v2", true, "v2", true},
		{"newer but invalid stays", "v1", "v2", false, "v1", false},
		{"no newer stays", "v2", "v2", true, "v2", false},
		{"unknown latest stays", "v1", "", true, "v1", false},
	}
	for _, c := range cases {
		pin, bumped := NextPin(c.current, c.latest, c.usable)
		if pin != c.wantPin || bumped != c.wantBumped {
			t.Errorf("%s: NextPin(%q,%q,%v) = (%q,%v), want (%q,%v)",
				c.name, c.current, c.latest, c.usable, pin, bumped, c.wantPin, c.wantBumped)
		}
	}
}
