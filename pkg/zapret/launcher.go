package zapret

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
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
//
// A SURVIVING start carries it too, via Said, and that half was missing. Between
// "the engine refused to run" and "the engine ran perfectly" lies the case that
// matters most here: it started, printed a complaint about an argument it does
// not understand, ignored that argument and carried on. The measurement then
// describes a strategy minus whatever the binary silently dropped, and scores it
// under the full recipe's name — principle 2, with the evidence thrown away by
// the only code that ever held it. The owner reports that Zapret-Manager's own
// strategies "would not come up" for him; our launcher says they start; the
// engine's own words are the only thing that can settle which.
func ExecLauncher(settle time.Duration) Launcher {
	return ExecLauncherSaying(settle, nil)
}

// ExecLauncherSaying is ExecLauncher plus a sink for what the engine said while
// it was starting, called only when the start SUCCEEDED and only when it said
// something. Silence stays silent; a complaint reaches a log.
func ExecLauncherSaying(settle time.Duration, said func(string)) Launcher {
	if settle <= 0 {
		settle = 400 * time.Millisecond
	}
	return func(ctx context.Context, bin, dir string, argv []string) (func(), error) {
		cmd := exec.Command(bin, argv...)
		if dir != "" {
			cmd.Dir = dir // bare payload filenames resolve against it
		}
		out := &syncBuf{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("%s: %w", bin, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case err := <-done:
			return nil, fmt.Errorf("engine exited immediately (%v): %s", err, strings.TrimSpace(out.String()))
		case <-time.After(settle):
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			return nil, ctx.Err()
		}
		// It survived. What it printed on the way up is still worth hearing: an
		// engine that starts, warns about an option it does not understand and runs
		// without it produces a measurement of a strategy it never applied.
		if said != nil {
			if msg := strings.TrimSpace(out.String()); msg != "" {
				said(msg)
			}
		}
		stop := func() {
			_ = cmd.Process.Kill()
			<-done
		}
		return stop, nil
	}
}

// syncBuf is a writer the child process fills from its own goroutine while the
// launcher reads it from this one.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
