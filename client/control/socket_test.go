package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSocketPathPrefersTheExistingPerUserSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	perUser := filepath.Join(dir, "lotsman", "control.sock")
	if err := os.MkdirAll(filepath.Dir(perUser), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(perUser, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultSocketPath(); got != perUser {
		t.Errorf("DefaultSocketPath() = %q, want the existing per-user socket %q", got, perUser)
	}
}

func TestDefaultSocketPathFallsBackToPerUserWhenNothingExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	perUser := filepath.Join(dir, "lotsman", "control.sock")
	// Neither the per-user socket nor (in a normal test env) the system one exists,
	// so a fresh same-user run should still get the per-user path to bind.
	if got := DefaultSocketPath(); got != perUser {
		t.Errorf("DefaultSocketPath() = %q, want per-user fallback %q", got, perUser)
	}
}
