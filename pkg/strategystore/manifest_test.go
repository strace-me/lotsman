package strategystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.yaml")
	want := Manifest{ID: "alt12", Name: "ALT12", Engine: "nfqws2", Args: "command.args"}
	if err := WriteManifest(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Engine != want.Engine || got.Args != want.Args {
		t.Fatalf("manifest = %#v, want %#v", got, want)
	}
}

func TestManifestRejectsAbsoluteArgs(t *testing.T) {
	err := (Manifest{ID: "alt12", Engine: "nfqws2", Args: "/etc/passwd"}).Validate()
	if err == nil {
		t.Fatal("absolute args path was accepted")
	}
}

func TestManifestWriteDoesNotLeaveTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "strategy.yaml")
	if err := WriteManifest(path, Manifest{ID: "alt12", Engine: "nfqws2", Args: "command.args"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains: %v", err)
	}
}
