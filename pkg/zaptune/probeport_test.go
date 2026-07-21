package zaptune

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

func TestProbePort(t *testing.T) {
	cases := map[string]int{
		"https://discord.com/api/v9/gateway": 443,
		"http://example.com/generate_204":    80,
		"https://example.com:8443/x":         8443,
		"stun.l.google.com:19302":            19302,
		"":                                   0,
	}
	for target, want := range cases {
		if got := ProbePort(registry.Service{ProbeTarget: target}); got != want {
			t.Errorf("ProbePort(%q) = %d, want %d", target, got, want)
		}
	}
}

func recipeWith(args ...string) strategycat.Recipe { return strategycat.Recipe{NfqwsArgs: args} }

func TestRecipeCoversPort(t *testing.T) {
	// Discord's media-port recipes cannot touch a probe of the API on 443. Judging
	// them on that probe would blame a strategy the measurement never exercised.
	media := recipeWith("--filter-tcp=2053,2083,2087,2096,8443", "--dpi-desync=fake")
	if recipeCoversPort(media, 443) {
		t.Error("a media-port recipe must not be offered for a 443 probe")
	}
	if !recipeCoversPort(media, 8443) {
		t.Error("8443 IS in the list and must be covered")
	}

	general := recipeWith("--filter-tcp=80,443", "--dpi-desync=multisplit")
	if !recipeCoversPort(general, 443) {
		t.Error("80,443 must cover 443")
	}

	// Ranges, as used by the voice profiles.
	ranged := recipeWith("--filter-tcp=19294-19344")
	if !recipeCoversPort(ranged, 19300) || recipeCoversPort(ranged, 443) {
		t.Error("range membership mishandled")
	}

	// No port filter at all matches everything, so it is always testable.
	if !recipeCoversPort(recipeWith("--dpi-desync=fake"), 443) {
		t.Error("an unfiltered recipe covers any port")
	}

	// An unknown probe port must not filter anything out — guessing would be worse
	// than leaving the choice alone.
	if !recipeCoversPort(media, 0) {
		t.Error("an unknown probe port must not exclude candidates")
	}
}
