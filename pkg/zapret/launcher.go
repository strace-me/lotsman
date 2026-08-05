package zapret

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecLauncher runs the engine as a child process and reports it started only
// after it has survived settle.
//
// The wait is the whole point. nfqws validates its inputs AFTER startup — it
// re-reads its hostlists once it has dropped privileges and exits if it cannot —
// so a spawn that returned nil immediately would hand the tuner a dead engine to
// measure through, and every candidate would score the same: whatever the
// uplink does with no desync at all.
//
// A failure carries the engine's own output, because an exit status says nothing
// while nfqws names the file it could not read.
func ExecLauncher(settle time.Duration) Launcher {
	if settle <= 0 {
		settle = 400 * time.Millisecond
	}
	return func(ctx context.Context, bin, dir string, argv []string) (func(), error) {
		cmd := exec.Command(bin, argv...)
		if dir != "" {
			cmd.Dir = dir // bare payload filenames resolve against it
		}
		var said strings.Builder
		cmd.Stdout, cmd.Stderr = &said, &said
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("%s: %w", bin, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case err := <-done:
			return nil, fmt.Errorf("engine exited immediately (%v): %s", err, strings.TrimSpace(said.String()))
		case <-time.After(settle):
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			return nil, ctx.Err()
		}
		stop := func() {
			_ = cmd.Process.Kill()
			<-done
		}
		return stop, nil
	}
}
