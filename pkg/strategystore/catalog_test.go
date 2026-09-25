package strategystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirSortsEngineAndID(t *testing.T) {
	root := t.TempDir()
	for _, spec := range []struct{ engine, id string }{{"nfqws2", "z-last"}, {"nfqws1", "b-two"}, {"nfqws1", "a-one"}} {
		path := filepath.Join(root, spec.engine, spec.id)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteBundle(path, Manifest{ID: spec.id, Name: spec.id, Engine: spec.engine, Args: "command.args"}, []string{"--dpi-desync=fake"}); err != nil {
			t.Fatal(err)
		}
	}
	bundles, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(bundles))
	for i, bundle := range bundles {
		got[i] = bundle.Manifest.Engine + "/" + bundle.Manifest.ID
	}
	want := []string{"nfqws1/a-one", "nfqws1/b-two", "nfqws2/z-last"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("catalog = %v, want %v", got, want)
		}
	}
}

func TestLoadDirRejectsEngineMismatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nfqws1", "alt12")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(path, Manifest{ID: "alt12", Engine: "nfqws2", Args: "command.args"}, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(root); err == nil {
		t.Fatal("engine mismatch was accepted")
	}
}
