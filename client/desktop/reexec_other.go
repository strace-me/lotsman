//go:build !unix

package main

import "log/slog"

// reexec is unix-only (syscall.Exec). Elsewhere the config was written but applying
// it needs a manual restart of the service.
func reexec(log *slog.Logger) {
	log.Warn("config saved; auto-restart is unix-only — restart the service to apply the new config")
}
