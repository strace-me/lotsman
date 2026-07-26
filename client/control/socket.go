package control

import (
	"os"
	"path/filepath"
)

// DefaultSocketPath is where the service puts its control socket when not told
// otherwise: $XDG_RUNTIME_DIR/lotsman/control.sock, falling back to the temp dir.
// The GUI and the tray both default here so they find a service started with no
// -control-socket flag. It mirrors client/desktop/main.go's own default.
func DefaultSocketPath() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "lotsman", "control.sock")
}
