// Package winws runs zapret's Windows desync engine as a managed child process.
//
// It is the Windows counterpart of client/platform/nfqws, and the difference is
// narrower than it looks. The desync FLAGS are identical — winws and nfqws share
// zapret's strategy language, which is why pkg/strategyimport drops the capture
// arguments from every recipe it reads and why the same catalog entry describes
// the Windows and Linux editions of a strategy. What differs is how traffic
// reaches the engine: nfqws is fed by an nft NFQUEUE rule that Lotsman installs,
// while winws carries its own WinDivert filter on its command line. So there is
// no ruleset to install or tear down here — the capture spec becomes argv.
//
// The invocation follows what the bundles actually do (see the Flowseal fixture
// in pkg/strategyimport/testdata): winws.exe lives beside its fake-payload .bin
// files and the launcher chdirs there, so recipes can name payloads by bare
// filename. This engine sets the working directory for the same reason.
//
// UNPROVEN: nothing in this package has met a real WinDivert. The process
// management, the argv it assembles and the keep-last-good behaviour are unit
// tested, and the flag spellings come from a real bundle — but "winws starts and
// desyncs traffic on a Windows box" is a claim no one has tested yet.
package winws

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/zapret"
)

// portToken is a single port or an inclusive range, the only two forms zapret's
// filter syntax accepts. Validating here means a malformed capture is named at
// the seam that produced it, rather than becoming an engine that refuses to start
// for a reason the operator has to go and read out of winws's own output.
var portToken = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?$`)

// CaptureArgs renders an instance's capture spec as WinDivert filter arguments.
//
// An EMPTY capture is an error rather than an empty filter: winws with no --wf-*
// selects nothing, so the engine would start, report itself healthy and desync
// not one packet — the silent-success failure this project keeps finding.
func CaptureArgs(c zapret.Capture) ([]string, error) {
	var out []string
	for _, spec := range []struct {
		flag  string
		ports []string
	}{
		{"--wf-tcp", c.TCP},
		{"--wf-udp", c.UDP},
	} {
		if len(spec.ports) == 0 {
			continue
		}
		clean := make([]string, 0, len(spec.ports))
		for _, p := range spec.ports {
			p = strings.TrimSpace(p)
			if !portToken.MatchString(p) {
				return nil, fmt.Errorf("winws: capture %s: %q is not a port or range", spec.flag, p)
			}
			clean = append(clean, p)
		}
		out = append(out, spec.flag+"="+strings.Join(clean, ","))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("winws: the capture spec names no ports, so the engine would filter nothing at all")
	}
	return out, nil
}

// Engine owns one winws process.
type Engine struct {
	bin     string
	inst    zapret.Instance
	workDir string // holds winws.exe and the fake payloads; recipes name them bare
	log     *slog.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	lastArgs []string
	// settle is how long the engine must survive to count as started. winws
	// validates its arguments and opens the WinDivert handle after start, so a
	// refusal shows up as an exit a moment later, not as a failed Start.
	settle time.Duration
}

// New returns an Engine for one winws instance. bin "" resolves winws.exe on
// PATH. workDir is the directory winws runs in — the one holding its .bin
// payloads — and "" inherits ours.
func New(bin string, inst zapret.Instance, workDir string, log *slog.Logger) *Engine {
	if bin == "" {
		bin = "winws.exe"
	}
	return &Engine{bin: bin, inst: inst, workDir: workDir, log: log}
}

// SetSettle overrides how long a launch waits before calling the engine alive.
// Exposed for tests, which otherwise race the default window and read a refusing
// engine as a started one.
func (e *Engine) SetSettle(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settle = d
}

// Apply (re)starts winws with args — the argv produced by nfqwsgen.Compose, which
// carries no capture flags of its own. Re-applying the SAME args is a no-op: a
// restart drops the desync mid-flow, so it must only happen on a real change.
//
// restarted reports whether this call actually relaunched, decided inside the
// lock from the real process state rather than from a snapshot the caller took
// earlier — so a crash racing this call cannot make a real restart look free.
func (e *Engine) Apply(ctx context.Context, args []string) (restarted bool, err error) {
	if len(args) == 0 {
		return false, fmt.Errorf("winws: empty strategy (nothing to desync)")
	}
	capture, err := CaptureArgs(e.inst.Capture)
	if err != nil {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cmd != nil && slices.Equal(e.lastArgs, args) {
		return false, nil // already running this exact strategy
	}
	// Remember what was working before letting go of it. WinDivert gives one
	// handle per filter, so the replacement cannot be proven alongside the
	// incumbent — the old process has to die first, which is exactly why the
	// previous argv has to survive the attempt.
	previous := slices.Clone(e.lastArgs)
	e.stopProcessLocked()

	if err := e.launchLocked(capture, args); err != nil {
		if len(previous) == 0 {
			return false, err // nothing was running; there is nothing to keep
		}
		if rbErr := e.launchLocked(capture, previous); rbErr != nil {
			return false, fmt.Errorf("winws: the new strategy failed AND the previous one no longer starts: %w (rollback: %v)", err, rbErr)
		}
		return false, fmt.Errorf("winws: kept the previous strategy, the new one would not start: %w", err)
	}
	return true, nil
}

func (e *Engine) settleFor() time.Duration {
	if e.settle > 0 {
		return e.settle
	}
	return 400 * time.Millisecond
}

// launchLocked starts winws and reports whether it was still alive a moment
// later. It records e.cmd/e.lastArgs only on success, so a failed attempt leaves
// the engine's recorded identity matching what is actually running.
func (e *Engine) launchLocked(capture, args []string) error {
	full := append(slices.Clone(capture), args...)
	cmd := exec.Command(e.bin, full...)
	cmd.Dir = e.workDir
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("winws: start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// It exited within the settle window, which for this engine means it
		// refused: winws that accepted its arguments runs until it is killed.
		return fmt.Errorf("winws: exited immediately (%v): %s", err, stderr.String())
	case <-time.After(e.settleFor()):
	}
	e.cmd = cmd
	e.lastArgs = slices.Clone(args)
	go e.reap(cmd, done, &stderr)
	e.log.Info("desync engine started", "engine", "winws", "capture", capture, "blocks", strings.Count(strings.Join(args, " "), "--new")+1)
	return nil
}

// reap notices an engine that dies later and clears the recorded identity, so
// Alive() answers from what happened rather than from what was once started.
func (e *Engine) reap(cmd *exec.Cmd, done <-chan error, stderr *tail) {
	err := <-done
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd != cmd {
		return // already replaced by a newer launch; not our business
	}
	e.cmd, e.lastArgs = nil, nil
	e.log.Error("desync engine exited on its own", "engine", "winws", "err", err, "stderr", stderr.String())
}

// Stop kills the process. There is no ruleset to remove: winws's filter lives and
// dies with it, which is the one way this engine is simpler than the Linux one.
func (e *Engine) Stop(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopProcessLocked()
	return nil
}

// Alive reports whether a winws process is running now.
func (e *Engine) Alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd != nil
}

func (e *Engine) stopProcessLocked() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	e.cmd, e.lastArgs = nil, nil
}

// tail keeps the last of a stream, so a refusal's message survives without
// buffering an engine's whole lifetime of output.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailMax = 4096

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailMax {
		t.buf = t.buf[len(t.buf)-tailMax:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
