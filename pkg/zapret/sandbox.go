package zapret

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Runner executes a command (executor.ExecRunner satisfies it); injected so the
// sandbox is testable without touching a real nft table.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Launcher starts a desync engine and returns a function that stops it. It
// reports an error when the engine did not survive startup — nfqws validates its
// inputs after it has dropped privileges, so "the process spawned" proves
// nothing and only survival does.
type Launcher func(ctx context.Context, bin, dir string, argv []string) (stop func(), err error)

// Sandbox measures one candidate desync strategy against live DPI without
// changing what anyone else's traffic gets.
//
// It owns two things: a small nft table that queues ONLY probe-marked egress to
// its own NFQUEUE, and a second engine bound to that queue running the candidate.
// The production table lets the mark through (GenerateNft emits the skip rule),
// so a marked probe is desynced exactly once, by the candidate, and everyone
// else's traffic never meets it.
//
// Apply has the signature desynctune.TuneService wants, so the tuner drives this
// directly.
type Sandbox struct {
	Opts   IsolateOptions
	Bin    string // engine binary
	Dir    string // working dir, so bare payload filenames resolve
	Run    Runner
	Launch Launcher
	// WAN re-resolves the egress interface each time the table is installed. The
	// table matches `oifname`, and a laptop that roams changes it — leaving a
	// sandbox that queues nothing, so every candidate reads "carried nothing at
	// all" and the recipes get blamed for the lane being pointed at a dead
	// interface. Production's engine was fixed the same way; the lane kept the
	// value it read at first lift. Optional: nil keeps Opts.WAN.
	WAN      func() string
	Log      *slog.Logger
	ProdQNum int // production's queue; the sandbox refuses to share it

	mu        sync.Mutex
	installed bool
	stop      func()
}

// Apply puts the candidate strategy into the sandbox, installing the table on
// first use. A candidate the engine will not start on is an error, not a silent
// no-op: the tuner would otherwise measure the PREVIOUS candidate and credit
// this one.
func (s *Sandbox) Apply(ctx context.Context, args []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Opts.QNum == s.ProdQNum {
		return fmt.Errorf("zapret: sandbox queue %d is production's; a candidate would take over real traffic", s.Opts.QNum)
	}
	// Table first, always. If the engine bound its queue before the table
	// existed, marked probe traffic would fall straight through to production
	// and the measurement would describe the incumbent strategy.
	if !s.installed {
		if err := s.installLocked(ctx); err != nil {
			s.removeProbeRoute(ctx)
			return err
		}
		s.installed = true
	}
	s.stopEngineLocked()

	// No args is the BASELINE, not a mistake: every A/B starts by measuring the
	// path with no desync at all, and without it there is nothing to compare a
	// candidate against. The table stays up so the probe is still isolated by its
	// mark; with no engine on the queue, `flags bypass` lets the marked packets
	// through untouched — which is exactly what "no desync" means here.
	//
	// Rejecting this cost the first live pass: tester.Resolve applies the baseline
	// arm before anything else, so the search died on its first step.
	if len(args) == 0 {
		s.log().Info("desync sandbox: baseline (no engine on the queue)", "qnum", s.Opts.QNum)
		return nil
	}

	argv := append([]string{fmt.Sprintf("--qnum=%d", s.Opts.QNum)}, args...)
	stop, err := s.Launch(ctx, s.Bin, s.Dir, argv)
	if err != nil {
		return fmt.Errorf("zapret: sandbox: candidate did not start: %w", err)
	}
	s.stop = stop
	// What the candidate actually is, without the payload paths that make the line
	// unreadable. A pass that measures the wrong thing is diagnosed from here:
	// without it the log shows a verdict and no way to tell which strategy earned
	// it.
	s.log().Info("desync sandbox: candidate applied", "qnum", s.Opts.QNum, "strategy", s.Describe(args))
	return nil
}

// Close stops the candidate and removes the table. Both are attempted even if
// the first fails: a sandbox that half-tears-down leaves either an engine nobody
// tracks or a table quietly diverting marked packets, and the next run would
// measure through the leftovers.
func (s *Sandbox) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopEngineLocked()
	s.removeProbeRoute(ctx)
	if !s.installed {
		return nil
	}
	s.installed = false
	if err := s.Run.Run(ctx, "nft", "delete", "table", s.Opts.Table); err != nil {
		return fmt.Errorf("zapret: sandbox: leftover table %q: %w", s.Opts.Table, err)
	}
	s.log().Info("desync sandbox down", "table", s.Opts.Table)
	return nil
}

func (s *Sandbox) installLocked(ctx context.Context) error {
	// Re-read the egress interface HERE, where the rule that names it is rendered,
	// rather than trusting whatever was true when this Sandbox was built.
	if s.WAN != nil {
		if wan := s.WAN(); wan != "" && wan != s.Opts.WAN {
			s.log().Info("desync sandbox: egress interface changed, re-scoping the lane",
				"was", s.Opts.WAN, "now", wan)
			s.Opts.WAN = wan
		}
	}
	// The candidate engine's INJECTED packets carry nfqws's own fwmark, and they
	// must stay out of the tunnel for the same reason production's do (see
	// DesyncFwmark). Ensured HERE because the lane is what runs while production is
	// unarmed — the exact window in which a missing rule turns a working recipe
	// into a false negative.
	_ = s.Run.Run(ctx, "ip", "rule", "del", "fwmark", fmt.Sprintf("0x%x", DesyncFwmark), "lookup", "main")
	if err := s.Run.Run(ctx, "ip", "rule", "add", "fwmark", fmt.Sprintf("0x%x", DesyncFwmark), "lookup", "main", "pref", "100"); err != nil {
		return fmt.Errorf("zapret: sandbox: keep the desync fwmark out of the tunnel: %w", err)
	}
	probeMark := fmt.Sprintf("0x%x", s.mark())
	_ = s.Run.Run(ctx, "ip", "rule", "del", "fwmark", probeMark, "lookup", "main")
	if err := s.Run.Run(ctx, "ip", "rule", "add", "fwmark", probeMark, "lookup", "main", "pref", "101"); err != nil {
		return fmt.Errorf("zapret: sandbox: keep the probe fwmark on the physical route: %w", err)
	}
	// A table from a killed run would otherwise stack with this one.
	_ = s.Run.Run(ctx, "nft", "delete", "table", s.Opts.Table)
	f, err := os.CreateTemp("", "lotsman-sandbox-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(GenerateIsolateNft(s.Opts)); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := s.Run.Run(ctx, "nft", "-f", f.Name()); err != nil {
		return fmt.Errorf("zapret: sandbox: install %q: %w", s.Opts.Table, err)
	}
	s.log().Info("desync sandbox up", "table", s.Opts.Table, "qnum", s.Opts.QNum, "mark", fmt.Sprintf("0x%x", s.mark()))
	return nil
}

func (s *Sandbox) removeProbeRoute(ctx context.Context) {
	mark := fmt.Sprintf("0x%x", s.mark())
	_ = s.Run.Run(ctx, "ip", "rule", "del", "fwmark", mark, "lookup", "main")
}

func (s *Sandbox) stopEngineLocked() {
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
}

func (s *Sandbox) mark() int {
	if s.Opts.Mark == 0 {
		return TuneMark
	}
	return s.Opts.Mark
}

func (s *Sandbox) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// Describe is a LOSSY summary for a log line: desync modes and filters only.
// It drops hostlists, payload paths and everything else, which is what makes it
// readable — and what makes it useless as evidence. Reading it as the argv cost
// an exchange: `--hostlist` is absent from every Describe output, and its absence
// was taken for the candidate being composed without one. Compare argv against
// the full record the caller logs, never against this.
func (s *Sandbox) Describe(args []string) string {
	var keep []string
	for _, a := range args {
		if strings.HasPrefix(a, "--dpi-desync") || strings.HasPrefix(a, "--filter") {
			keep = append(keep, a)
		}
	}
	return strings.Join(keep, " ")
}
