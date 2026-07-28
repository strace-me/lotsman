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
	perUser := filepath.Join(base, "lotsman", "control.sock")
	if _, err := os.Stat(perUser); err == nil {
		return perUser
	}
	if _, err := os.Stat(SystemSocketPath); err == nil {
		return SystemSocketPath
	}
	return perUser
}
