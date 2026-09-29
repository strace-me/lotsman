package configv2

import "testing"

// limit and min_coverage are a RANKING's knobs. On a url_test pool they would read
// as "the cap was applied" while sing-box keeps choosing by latency — a setting
// that looks live and does nothing. That is the failure this project has an axiom
// about, so it is a load error rather than a silently inert field.
func TestRankingKnobsAreRefusedOnASelectorPool(t *testing.T) {
	base := func() *Configuration {
		return &Configuration{Pools: map[string]Pool{
			"bulk": {Type: "speedtest_top", Limit: 3, MinCoverage: 0.5},
		}}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("a speedtest_top pool with ranking knobs was refused: %v", err)
	}

	cfg := base()
	cfg.Pools["sel"] = Pool{Type: "url_test", Limit: 3}
	if err := cfg.Validate(); err == nil {
		t.Error("limit on a url_test pool was accepted")
	}
	cfg = base()
	cfg.Pools["sel"] = Pool{Type: "url_test", MinCoverage: 0.5}
	if err := cfg.Validate(); err == nil {
		t.Error("min_coverage on a url_test pool was accepted")
	}
}

// "DE" or "germany" must not load: lower-casing happens on projection, and a
// value that matches nothing looks exactly like a filter that removed nothing.
func TestCountryFilterWantsISOAlphaTwo(t *testing.T) {
	cfg := &Configuration{Pools: map[string]Pool{
		"bulk": {Type: "url_test", CountriesExclude: []string{"ru", "de"}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid country codes were refused: %v", err)
	}
	for _, bad := range []string{"DE", "germany", "d", "deu"} {
		cfg = &Configuration{Pools: map[string]Pool{
			"bulk": {Type: "url_test", CountriesExclude: []string{bad}},
		}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("country %q was accepted", bad)
		}
	}
}

func TestMinCoverageMustBeAFraction(t *testing.T) {
	for _, bad := range []float64{-0.1, 1.5} {
		cfg := &Configuration{Pools: map[string]Pool{
			"bulk": {Type: "speedtest_top", MinCoverage: bad},
		}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("min_coverage %v was accepted", bad)
		}
	}
}
