package control

import (
	"os"
	"path/filepath"
)

// SystemSocketPath is where the root systemd service (the daily-driver) puts its
// control socket: /run/lotsman/control.sock, group-owned so an unprivileged UI in
// the lotsman group can reach it. Only meaningful on Linux; on other platforms it
// simply won't exist and the per-user path is used.
const SystemSocketPath = "/run/lotsman/control.sock"

// DefaultSocketPath is where the GUI and tray look for a service when not told a
// path with -socket. A same-user run (proxy mode, dev) puts its socket under
// $XDG_RUNTIME_DIR/lotsman/control.sock (falling back to the temp dir), mirroring
// client/desktop/main.go's own default; that per-user path wins when it exists.
// Failing that — the common daily-driver case, where the service is a root systemd
// unit and the UI is unprivileged — it falls back to the system socket, so the GUI
// and tray find the service with no flag. If neither exists yet the per-user path
// is returned so a fresh same-user run still binds somewhere sensible.
func DefaultSocketPath() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return chooseSocketPath(filepath.Join(base, "lotsman", "control.sock"), SystemSocketPath)
}

// chooseSocketPath is the decision itself, with both candidates passed in so a
// test can stage them; DefaultSocketPath supplies the real ones.
func chooseSocketPath(perUser, system string) string {
	if _, err := os.Stat(perUser); err == nil {
		return perUser
	}
	// An error that is NOT "does not exist" is evidence the system socket IS there
	// and we are simply not allowed to look at it — the directory reaching it is
	// 0750 root:lotsman, so a process outside the group gets EACCES from the stat,
	// not ENOENT. Treating that as absence is what made a working service report
	// itself missing: a desktop session started before the group existed cannot
	// traverse /run/lotsman, so the GUI fell through to a per-user path that was
	// never going to exist and said "no such file or directory" about it.
	//
	// Choosing the guarded path instead means the dial fails with "permission
	// denied", which is the truth and names the real fix (log out and back in, so
	// the session picks up the lotsman group).
	if _, err := os.Stat(system); err == nil || !os.IsNotExist(err) {
		return system
	}
	return perUser
}
