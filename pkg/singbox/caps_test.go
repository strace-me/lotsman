package singbox

import "testing"

func TestCapsVersionGating(t *testing.T) {
	box := Capabilities("1.12.17") // the R5S box
	if !box.Supports(FeatureTLSFragment) || !box.Supports(FeatureUTLS) || !box.Supports(FeatureAnyTLS) {
		t.Error("1.12.17 should support tls_fragment, utls, anytls")
	}

	old := Capabilities("1.11.0")
	if old.Supports(FeatureTLSFragment) {
		t.Error("1.11.0 must NOT support tls_fragment (added 1.12.0) — knob should be gated off")
	}
	if old.Supports(FeatureAnyTLS) {
		t.Error("1.11.0 must NOT support anytls (added 1.12.0)")
	}
	if !old.Supports(FeatureUTLS) {
		t.Error("1.11.0 should still support utls (long-standing)")
	}
}

func TestCapsGeckoGatedTo114(t *testing.T) {
	// gecko is real but added in sing-box 1.14.0 — our 1.12.17 must NOT offer it,
	// a 1.14+ target may.
	if Capabilities("1.12.17").Supports(FeatureGeckoObfs) {
		t.Error("gecko must be gated off on 1.12.17 (added 1.14.0)")
	}
	if !Capabilities("1.14.0").Supports(FeatureGeckoObfs) {
		t.Error("gecko should be supported on 1.14.0+")
	}
}

func TestCapsEmptyVersionIsBaseline(t *testing.T) {
	if Capabilities("").Version() != baselineVersion {
		t.Errorf("empty version should resolve to baseline %s", baselineVersion)
	}
	if !Capabilities("").Supports(FeatureTLSFragment) {
		t.Error("baseline should support tls_fragment")
	}
}

func TestCapsUngatedFeatureDefaultsSupported(t *testing.T) {
	// A feature with no minVersion entry must default to supported (we only gate
	// knobs with a known version boundary; an ungated one is not silently blocked).
	if !Capabilities("1.0.0").Supports(Feature("not-in-table")) {
		t.Error("an ungated feature must default to supported, not silently blocked")
	}
}

// Every feature gated so far must light up on a newer target: the generator is
// forward-compatible, so a machine on 1.13/1.14/1.15 gets the full set the config
// asks for once that version is installed. This is the "supports a newer sing-box"
// guarantee — the version string is parsed, not matched against a fixed list.
func TestCapsNewerTargetsEnableEveryGatedFeature(t *testing.T) {
	// Everything but gecko is gated at or below 1.12; gecko lands in 1.14.
	base := []Feature{FeatureTLSFragment, FeatureUTLS, FeatureAnyTLS, FeatureFakeIP, FeatureDNSServers, FeatureMultiplex, FeatureShadowTLS, FeatureWireGuard, FeatureECH}
	c13 := Capabilities("1.13.0")
	for _, f := range base {
		if !c13.Supports(f) {
			t.Errorf("sing-box 1.13.0 should support %s", f)
		}
	}
	if c13.Supports(FeatureGeckoObfs) {
		t.Error("sing-box 1.13.0 must NOT support gecko (1.14+)")
	}
	for _, v := range []string{"1.14.0", "1.15.0", "1.15", "2.0.0"} {
		c := Capabilities(v)
		for _, f := range append(append([]Feature{}, base...), FeatureGeckoObfs) {
			if !c.Supports(f) {
				t.Errorf("sing-box %s should support %s", v, f)
			}
		}
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.12.17", "1.12.0", true},
		{"1.12.0", "1.12.0", true},
		{"1.11.9", "1.12.0", false},
		{"2.0.0", "1.12.0", true},
		{"1.12.17-alpha.3", "1.12.0", true},
		{"v1.13.0", "1.12.0", true},
		{"1.12", "1.12.0", true},
	}
	for _, c := range cases {
		if got := atLeast(c.a, c.b); got != c.want {
			t.Errorf("atLeast(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}
