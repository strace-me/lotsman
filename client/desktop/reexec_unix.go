//go:build unix

package main

import (
	"log/slog"
	"os"
	"syscall"
)

// reexec replaces this process with a fresh copy of itself (same argv + env), so a
// saved config is loaded cleanly without the non-re-entrant Core.Start. The caller
// has already torn the data plane down via Core.Stop, so no children are orphaned.
// It returns only if exec fails.
func reexec(log *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	log.Info("applying new config — re-executing", "exe", exe)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		// The data plane is already down (Core.Stop ran before this) and we could not
		// come back. Exiting 0 here would tell systemd this was a clean shutdown, so
		// Restart=on-failure would leave the machine with no tunnel and nothing to
		// restart it. Exit non-zero: a failed recovery is a failure.
		log.Error("re-exec failed — exiting non-zero so the service manager restarts us", "err", err)
		os.Exit(1)
	}
}
