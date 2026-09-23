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
	// so a fresh same-user run should still get the per-user path to bind. The
	// system path is honed down to a per-test temp dir rather than the real
	// SystemSocketPath: the daily-driver service owns the real /run/lotsman socket,
	// and a test must not read its decisions from the machine it happens to run on.
	system := filepath.Join(t.TempDir(), "lotsman", "control.sock")
	if got := chooseSocketPath(perUser, system); got != perUser {
		t.Errorf("DefaultSocketPath() = %q, want per-user fallback %q", got, perUser)
	}
}

// The defect this guards: /run/lotsman is 0750 root:lotsman, so a process outside
// the group gets EACCES from the stat rather than ENOENT. Reading that as "absent"
// sent the GUI to a per-user path that was never going to exist, and it reported
// "no such file or directory" while the service was running perfectly — observed
// on the ThinkPad the day the systemd unit was installed, because the desktop
// session predated the group.
func TestDefaultSocketPathPrefersASystemSocketItCannotStat(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses any directory, so the permission case cannot be staged")
	}
	// A directory we may not traverse, standing in for /run/lotsman.
	dir := filepath.Join(t.TempDir(), "guarded")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatalf("stage guarded dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	guarded := filepath.Join(dir, "control.sock")
	if _, err := os.Stat(guarded); os.IsNotExist(err) {
		t.Skipf("this filesystem does not enforce directory traversal (stat gave %v)", err)
	}
	if got := chooseSocketPath("/nonexistent/per-user.sock", guarded); got != guarded {
		t.Errorf("chose %q; a path we are merely forbidden to look at is evidence the service IS there, not that it is missing", got)
	}
}

func TestDefaultSocketPathIgnoresAnAbsentSystemSocket(t *testing.T) {
	perUser := filepath.Join(t.TempDir(), "per-user.sock")
	if got := chooseSocketPath(perUser, filepath.Join(t.TempDir(), "nope.sock")); got != perUser {
		t.Errorf("chose %q, want the per-user path when neither exists", got)
	}
}
