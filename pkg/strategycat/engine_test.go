package strategycat

import "testing"

// The two engines share no flag vocabulary, so a recipe is only meaningful
// together with the engine it was written for. nfqws1 is the default so the
// existing entries keep their meaning without being rewritten.
func TestEngineOfDefaultsToNfqws1(t *testing.T) {
	if got := (Recipe{}).EngineOf(); got != "nfqws1" {
		t.Errorf("empty engine resolved to %q, want nfqws1", got)
	}
	if got := (Recipe{Engine: "nfqws2"}).EngineOf(); got != "nfqws2" {
		t.Errorf("explicit engine resolved to %q", got)
	}
}

// A recipe must not be selected for an engine whose flags it does not speak.
// This is the check that keeps an nfqws2 recipe from being handed to nfqws1,
// where `--lua-desync=...` is not a known argument and the strategy quietly
// desyncs nothing.
func TestCatalogRecipesAllCarryAnEngine(t *testing.T) {
	known := map[string]bool{"nfqws1": true, "nfqws2": true}
	for _, r := range Load() {
		engine := r.EngineOf()
		if !known[engine] {
			t.Errorf("recipe %q names unknown engine %q", r.ID, engine)
		}
	}
}

func TestLoadedRecipesParseTheEngineField(t *testing.T) {
	// The catalog is generated; a field the Go struct cannot read would be dropped
	// in silence on every load, which is how a strategy ends up on the wrong engine.
	for _, r := range Load() {
		if r.Engine == "" {
			continue // defaults to nfqws1
		}
		if r.EngineOf() != r.Engine {
			t.Fatalf("recipe %q: engine field was not read back", r.ID)
		}
	}
}
