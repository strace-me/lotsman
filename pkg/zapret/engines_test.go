package zapret

import "testing"

func TestStrategyEngineAdapters(t *testing.T) {
	adapters := StrategyEngineAdapters()
	for _, name := range []string{"nfqws1", "nfqws2"} {
		if _, ok := adapters[name]; !ok {
			t.Fatalf("adapter %q is missing", name)
		}
	}
}
