package reconcile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBaselineRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	s := NewBaselineStore(path)

	// Missing file -> 0 (cold start), no error.
	if got := s.Load(); got != 0 {
		t.Fatalf("cold load = %d, want 0", got)
	}
	if err := s.Save(17); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := s.Load(); got != 17 {
		t.Errorf("loaded = %d, want 17", got)
	}
}

func TestBaselineCorruptIsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewBaselineStore(path).Load(); got != 0 {
		t.Errorf("corrupt load = %d, want 0 (best-effort)", got)
	}
}
