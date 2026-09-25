package strategystore

import "testing"

func TestEngineFixturesLoad(t *testing.T) {
	bundles, err := LoadDir("testdata/strategies")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 2 {
		t.Fatalf("bundle count = %d, want 2", len(bundles))
	}
	if bundles[0].Manifest.Engine != "nfqws1" || bundles[1].Manifest.Engine != "nfqws2" {
		t.Fatalf("bundle engines = %q, %q", bundles[0].Manifest.Engine, bundles[1].Manifest.Engine)
	}
}
