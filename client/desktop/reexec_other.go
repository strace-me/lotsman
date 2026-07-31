//go:build !unix

package main

import (
	"log/slog"
	"os"
)

// reexec is unix-only (syscall.Exec). Elsewhere the config was written but applying
// it needs a manual restart of the service.
func reexec(log *slog.Logger) {
	// Same reasoning as the unix path, and it bites harder here: there is no exec at
	// all, so every fallback-to-re-exec save would otherwise tear the data plane down
	// and exit cleanly, leaving a service manager with no reason to bring it back.
	log.Error("config saved but auto-restart is unix-only — exiting non-zero so the service manager restarts us")
	os.Exit(1)
}
