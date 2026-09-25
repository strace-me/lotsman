package strategystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreKeepsLastGoodSnapshotOnReloadError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nfqws1", "alt12")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(path, Manifest{ID: "alt12", Engine: "nfqws1", Args: "command.args"}, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(root)
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 1 {
		t.Fatalf("initial snapshot = %d", len(store.List()))
	}
	if err := os.Remove(filepath.Join(path, "command.args")); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("broken reload succeeded")
	}
	if len(store.List()) != 1 {
		t.Fatalf("last good snapshot = %d", len(store.List()))
	}
}
