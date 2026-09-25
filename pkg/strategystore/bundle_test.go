package strategystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundleRoundTrip(t *testing.T) {
	root := t.TempDir()
	manifest := Manifest{
		ID: "alt12", Name: "ALT12", Engine: "nfqws2", Args: "command.args",
		Assets: map[string]string{"lua": "lua"},
	}
	if err := os.MkdirAll(filepath.Join(root, "lua"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(root, manifest, []string{"--lua-desync=lua/fake.lua"}); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.ID != "alt12" || len(bundle.Args.Values) != 1 {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestLoadBundleRejectsMissingAsset(t *testing.T) {
	root := t.TempDir()
	manifest := Manifest{ID: "alt12", Name: "ALT12", Engine: "nfqws2", Args: "command.args", Assets: map[string]string{"payloads": "payloads"}}
	if err := WriteBundle(root, manifest, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root); err == nil {
		t.Fatal("bundle with missing asset was accepted")
	}
}
