package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewFileStore(path)

	// Missing file -> empty, no error.
	if got := s.Load(); len(got) != 0 {
		t.Fatalf("cold load = %v, want empty", got)
	}

	if err := s.Save(map[string]int{"youtube": 2, "discord": 0}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := s.Load()
	if got["youtube"] != 2 || got["discord"] != 0 {
		t.Errorf("loaded = %v, want youtube=2 discord=0", got)
	}
}

func TestLoadCorruptIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewFileStore(path).Load(); len(got) != 0 {
		t.Errorf("corrupt load = %v, want empty (best-effort)", got)
	}
}
