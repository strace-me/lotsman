//go:build !unix

package main

import (
	"log/slog"
	"os"
)

// reexec applies a saved config by starting a FRESH copy of this process and letting
// this one exit — the closest thing to syscall.Exec on a platform without it.
//
// The caller has already torn the data plane down (Core.Stop), so the ports, the tun
// and the control socket are free for the replacement and nothing is orphaned. Merely
// exiting — what this did before — leaves the machine with no tunnel and, on Windows,
// nothing registered with the service manager to bring it back: the GUI would report
// the config saved and applied while the client was in fact gone for good.
func reexec(log *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	log.Info("applying new config — starting a fresh copy", "exe", exe)
	p, err := os.StartProcess(exe, os.Args, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		// Nothing took our place and the data plane is already down. Exit non-zero so a
		// service manager, if one is watching, sees a failure rather than a clean stop.
		log.Error("could not start the replacement process; the new config is saved but NOT running", "err", err)
		os.Exit(1)
	}
	// Do not wait: the replacement owns the data plane from here.
	_ = p.Release()
}
