// Package nfqws runs the zapret nfqws desync engine on a Linux desktop: it
// installs the NFQUEUE nft rules, launches nfqws as a managed child process, and
// tears both down on stop.
//
// The router does the same job through a generated shell launcher + an "active"
// symlink + procd (pkg/zapret, pkg/executor.NewZapret). A desktop client owns the
// process directly instead, which also lets it pass the composed strategy as an
// argv SLICE rather than a /bin/sh command line — removing the shell-injection
// surface the script path has to defend against when it inlines rule_set-derived
// domains.
//
// The nft ruleset itself is NOT reinvented here: it is rendered by
// zapret.GenerateNft, the same function the router's init script uses, so both
// deployments queue identical traffic to identical queue numbers.
package nfqws

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/zapret"
)

// Engine owns one nfqws instance and its nft table.
type Engine struct {
	bin  string
	inst zapret.Instance
	// baseCapture is the spec the engine was built with — the floor the derived
	// capture is unioned onto, so recomposition can widen the queue but never
	// narrow it below the ports the deployment always wants carried.
	baseCapture zapret.Capture
	nftOpts     zapret.NftOptions
	fakeDir     string // working dir for nfqws so its bare fake-payload filenames resolve; "" = inherit ours
	log         *slog.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	lastArgs []string
	armed    bool   // nft table installed
	crashLog string // file that unexpected exits are appended to; "" = log only
	// settle is how long the engine must survive to count as started. nfqws
	// validates its inputs AFTER dropping privileges, so this window is the
	// measurement, not a courtesy — too short and a refusal is read as a success.
	settle time.Duration
}

// SetSettle overrides how long a launch waits before calling the engine alive.
// Exposed for tests, which otherwise race the default window on a loaded machine
// and report a refusing engine as started.
func (e *Engine) SetSettle(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settle = d
}

// New returns an Engine for one nfqws instance. bin "" resolves nfqws on PATH.
// fakeDir is nfqws's working dir, holding the fake-payload files its strategies name
// by bare filename (tls_clienthello_*.bin, quic_initial_*.bin); "" inherits ours.
func New(bin string, inst zapret.Instance, nftOpts zapret.NftOptions, fakeDir string, log *slog.Logger) *Engine {
	if bin == "" {
		bin = "nfqws"
	}
	return &Engine{bin: bin, inst: inst, baseCapture: inst.Capture, nftOpts: nftOpts, fakeDir: fakeDir, log: log}
}

// Apply installs the nft rules (idempotent) and (re)starts nfqws with args — the
// argv produced by nfqwsgen.Compose. Re-applying the SAME args is a no-op: a
// restart drops the desync mid-flow, so it must only happen on a real change.
//
// restarted reports whether this call actually (re)launched the process, so the
// caller can log the reload-free path honestly. It is decided INSIDE the lock
// from the real e.cmd state — not from a snapshot the caller took earlier — so a
// crash-and-reap racing this call cannot make a genuine restart look reload-free.
func (e *Engine) Apply(ctx context.Context, args []string) (restarted bool, err error) {
	if runtime.GOOS != "linux" {
		return false, fmt.Errorf("nfqws: NFQUEUE desync requires Linux (running on %s)", runtime.GOOS)
	}
	if len(args) == 0 {
		return false, fmt.Errorf("nfqws: empty strategy (nothing to desync)")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cmd != nil && slices.Equal(e.lastArgs, args) {
		return false, nil // already running this exact strategy — no restart
	}
	// The queue must carry exactly what the strategy filters on. Deriving it from
	// the argv rather than holding a second literal is what stops a profile from
	// being born dead: Discord voice filters udp 50000-50100, and against a
	// hardcoded capture of udp/443 it matched nothing and said nothing.
	if err := e.installNftLocked(ctx, args); err != nil {
		return false, err
	}
	// Remember what was working before letting go of it. nfqws binds one NFQUEUE,
	// so the replacement cannot be proven alongside the incumbent — the old
	// process has to die first, and that is exactly why the previous argv must
	// survive the attempt.
	previous := slices.Clone(e.lastArgs)
	e.stopProcessLocked()

	if err := e.launchLocked(args); err != nil {
		if len(previous) == 0 {
			return false, err // nothing was running; there is nothing to keep
		}
		if rbErr := e.launchLocked(previous); rbErr != nil {
			// Both are down. Say so with both engines' own words rather than
			// leaving the operator to infer it from a silent absence of desync.
			return false, fmt.Errorf("nfqws: the new strategy failed AND the previous one no longer starts: %w (rollback: %v)", err, rbErr)
		}
		return false, fmt.Errorf("nfqws: kept the previous strategy, the new one would not start: %w", err)
	}
	return true, nil
}

func (e *Engine) settleFor() time.Duration {
	if e.settle > 0 {
		return e.settle
	}
	return 400 * time.Millisecond
}

// launchLocked starts nfqws on args and reports whether it was still alive a
// moment later. It records e.cmd/e.lastArgs only on success, so a failed attempt
// leaves the engine's recorded identity matching whatever is actually running —
// this project's recurring defect is a field asserting a state nobody observed.
func (e *Engine) launchLocked(args []string) error {
	full := append([]string{fmt.Sprintf("--qnum=%d", e.inst.QNum)}, args...)
	cmd := exec.Command(e.bin, full...)
	// nfqws resolves fake-payload files (--dpi-desync-fake-tls=tls_clienthello_*.bin)
	// relative to its working dir. Point it at the dir that holds them, else it exits
	// immediately with "could not read <file>.bin".
	if e.fakeDir != "" {
		cmd.Dir = e.fakeDir
	}
	// Tee the engine's own output: it is the only thing that explains a refusal
	// (a missing payload, an unreadable hostlist after it drops privileges), and
	// without it a failure is just "exit status 1".
	var said tailBuffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &said)
	cmd.Stderr = io.MultiWriter(os.Stderr, &said)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nfqws: start %s: %w", e.bin, err)
	}
	// One Wait, shared: the liveness check and the reaper both read this channel.
	// Calling cmd.Wait twice is a race, and nil-ing cmd.Process would break Kill.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Confirm it is STILL alive a moment later. nfqws validates its inputs AFTER
	// startup — notably that it can re-read the hostlist once it has dropped
	// privileges — and exits if not. Reporting success here would be worse than
	// cosmetic: the caller's canary would judge a strategy that never ran, record a
	// failure against a blameless recipe, and demote its way through the whole
	// catalog on the strength of an engine that is not running.
	select {
	case err := <-done:
		return fmt.Errorf("nfqws: engine exited immediately (%v): %s", err, said.String())
	case <-time.After(e.settleFor()):
	}

	e.cmd = cmd
	e.lastArgs = slices.Clone(args)
	go func() {
		err := <-done
		e.mu.Lock()
		stopping := e.cmd != cmd // Stop/replace already took ownership; this exit was asked for
		if e.cmd == cmd {
			e.cmd = nil
			e.lastArgs = nil
		}
		e.mu.Unlock()
		if err == nil || stopping {
			return
		}
		// The engine died on its own. "exit status 1" says nothing; nfqws says exactly
		// what it refused, and it says it on the way out — so carry its own last words,
		// and the argv that provoked them, or the next reader is left guessing the way
		// the router's operator was for eight days.
		e.log.Warn("nfqws exited on its own", "err", err, "argv", strings.Join(full, " "), "said", said.String())
		e.recordCrash(full, err, said.String())
	}()
	e.log.Info("nfqws applied", "qnum", e.inst.QNum, "blocks", strings.Count(strings.Join(args, " "), "--new")+1)
	return nil
}

// Stop kills nfqws and removes the nft table, leaving the host as it was found.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopProcessLocked()
	if !e.armed {
		return nil
	}
	// Best-effort: a missing table is not an error, but a LEFT-BEHIND table would
	// silently keep queueing packets to a queue nobody reads, which black-holes
	// traffic. Report the failure loudly.
	if err := run(ctx, "nft", "delete", "table", e.nftOpts.Table); err != nil {
		e.log.Error("nfqws: could not remove nft table — traffic may still be queued", "table", e.nftOpts.Table, "err", err)
		return fmt.Errorf("nfqws: delete nft table: %w", err)
	}
	e.armed = false
	return nil
}

// Alive reports whether the nfqws process is currently running, read from the real
// child state (the reaper nils e.cmd on exit). This is the liveness /status needs:
// it is NOT the same as "a service sits on the zapret rung" — an uncovered plan
// never starts the process, and a crash leaves e.cmd nil while services are still
// assigned — so reporting the rung set as liveness would show green over a dead
// engine, the LOT-49 trap the sing-box gate exists to avoid.
func (e *Engine) Alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd != nil
}

// installNftLocked (re)installs this instance's nft table so it carries the ports
// the given strategy filters on. Caller holds e.mu.
//
// It re-installs when the required port set CHANGES, not just once: the strategy
// is recomposed whenever services move between rungs, and a new recipe may filter
// ports the current table does not queue. Leaving the table as installed would
// leave that profile receiving nothing — the silent half of the failure this
// derivation exists to remove.
func (e *Engine) installNftLocked(ctx context.Context, args []string) error {
	// The instance's own spec is a FLOOR, not the whole answer: it keeps the
	// ordinary web ports queued even at a moment when no composed profile happens
	// to name them.
	want := e.baseCapture.Union(zapret.CaptureFromArgs(args))
	if e.armed && e.inst.Capture.Equal(want) {
		return nil
	}
	if e.armed && !e.inst.Capture.Equal(want) {
		e.log.Info("nfqws: capture changed, reinstalling the queue",
			"tcp", want.TCP, "udp", want.UDP)
	}
	e.inst.Capture = want
	rules := zapret.GenerateNft([]zapret.Instance{e.inst}, e.nftOpts)
	// Drop a stale table from a previous run first; absence is fine.
	_ = run(ctx, "nft", "delete", "table", e.nftOpts.Table)
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nfqws: install nft rules: %w: %s", err, out)
	}
	e.armed = true
	e.log.Info("nfqws nft rules installed", "table", e.nftOpts.Table, "wan", e.nftOpts.WAN,
		"qnum", e.inst.QNum, "tcp", e.inst.Capture.TCP, "udp", e.inst.Capture.UDP)
	return nil
}

// stopProcessLocked kills the child if running. Caller holds e.mu.
func (e *Engine) stopProcessLocked() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		e.cmd = nil
		e.lastArgs = nil
	}
}

// tailBuffer keeps the last few KB of a stream — enough to carry the reason in
// an error without letting a chatty engine grow unbounded.
// SetCrashLog names a file that unexpected engine exits are appended to. Empty
// disables it and the exit is only logged. The file is the durable half: the log
// line goes wherever the service's stderr goes, which on a box nobody reads is
// nowhere, whereas this survives the client dying too.
func (e *Engine) SetCrashLog(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.crashLog = path
}

// recordCrash appends one record for an engine that exited by itself. Best effort:
// failing to write a diagnostic must never be louder than the fault it describes.
func (e *Engine) recordCrash(argv []string, exitErr error, said string) {
	e.mu.Lock()
	path := e.crashLog
	e.mu.Unlock()
	if path == "" {
		return
	}
	rec := fmt.Sprintf("=== %s  nfqws exited: %v\nargv: %s\n%s\n",
		time.Now().Format(time.RFC3339), exitErr, strings.Join(argv, " "), strings.TrimRight(said, "\n"))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		e.log.Warn("nfqws: could not write the crash record", "path", path, "err", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(rec); err != nil {
		e.log.Warn("nfqws: could not write the crash record", "path", path, "err", err)
	}
}

// tailBuffer keeps only the last 4KiB written to it — enough for nfqws's own
// complaint, bounded so a chatty engine cannot grow it without limit.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func run(ctx context.Context, name string, args ...string) error {
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DetectWAN returns the interface carrying the default route — the egress the
// nft rules must match. A laptop changes it on every roam, unlike the router's
// fixed eth0, so it is detected rather than configured.
func DetectWAN(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "ip", "route", "show", "default").Output()
	if err != nil {
		return "", fmt.Errorf("nfqws: detect WAN: %w", err)
	}
	if dev := ParseDefaultRouteIface(string(out)); dev != "" {
		return dev, nil
	}
	return "", fmt.Errorf("nfqws: no default route found")
}

// ParseDefaultRouteIface pulls the interface out of `ip route show default`
// output. Split out from the exec call so it is testable off-Linux. Only DEFAULT
// routes are considered: taking "dev" from any line would happily return the
// interface of a plain subnet route, and we would then install the NFQUEUE rules
// on the wrong egress — silently desyncing the wrong traffic, or none.
func ParseDefaultRouteIface(out string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "default" {
			continue
		}
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

// tunnelPrefixes are the interface names VPN data planes conventionally take.
var tunnelPrefixes = []string{"tun", "tap", "wg", "ppp", "utun", "nordlynx", "proton"}

// ForeignTunnels lists tunnel interfaces already present on the host. Called
// before this client creates its own, so anything found belongs to somebody else.
//
// It matters because the desync cannot tell a foreign tunnel's packets from
// ordinary traffic: sing-box's auto_route installs NO host route for its servers
// (verified — it uses policy rules and binds its own egress instead), so the
// peers of another VPN are not discoverable from the routing table, and nfqws
// would happily mangle the very packets carrying somebody's tunnel. That failure
// is silent: their VPN just degrades.
func ForeignTunnels() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		name := strings.ToLower(ifc.Name)
		for _, p := range tunnelPrefixes {
			if strings.HasPrefix(name, p) {
				out = append(out, ifc.Name)
				break
			}
		}
	}
	return out
}
