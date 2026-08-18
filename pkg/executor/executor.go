// Package executor adapts a strategy class to a concrete data-plane action.
// One StrategyExecutor per class (zapret, vpn, ...). M0 ships two; the
// interface is the seam that lets tpws/byedpi/dynamic-nfqws drop in later
// without touching Brain or Applier (the pluggable-executor pattern).
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// ErrDeferred is returned by an Enable that ACCEPTED the request but has not
// carried it out yet — the zapret executor hands a candidate to the gate, which
// proves it in the sandbox before any real traffic moves, and that outcome
// arrives later or not at all.
//
// It exists because a nil return meant "applied", and the applier logged exactly
// that: `applied service=youtube class=zapret strategy=ALT12` was written while
// the recipe was still queued for proof, and on the office network it went on to
// FAIL the canary. A line asserting a state must come from the code that observed
// it (principle 1), so the executor now says which of the two it means.
//
// It is not a failure: callers that treat it as one would suppress the
// actual-state report and spin.
var ErrDeferred = errors.New("accepted, not applied yet")

// StrategyExecutor routes a service's traffic through one mechanism. Enable
// must be idempotent: calling it twice with the same args is a no-op on the
// data plane's observable state. It returns ErrDeferred when the request was
// accepted but the data plane has not moved yet.
type StrategyExecutor interface {
	Class() string
	Enable(ctx context.Context, service, strategyID string) error
}

// ScriptSwitcher activates a DPI-bypass strategy by repointing a stable symlink
// (active.sh) at the requested strategy script and restarting the engine's
// service. It backs both the nfqws (zapret) and byedpi engines — they differ
// only in class, script dir, symlink, and init service. The engine process is
// global (one strategy for all its captured traffic), so this is a GLOBAL
// switch; the service arg is recorded for logs but does not scope it. The
// strategy ID names a script in scriptDir (e.g. "alt12" -> <dir>/alt12.sh).
//
// This is how self-healing within a bypass engine works: when a strategy stops
// working (DPI adapted), Brain escalates and the next step picks another
// strategy, which this executor activates.
// SelectorSetter points a sing-box selector at a target outbound over the Clash
// API. *dataplane.ClashClient satisfies it; abstracted so RouteToDirect is
// testable without HTTP.
type SelectorSetter interface {
	SetSelector(ctx context.Context, selector, target string) error
}

type ScriptSwitcher struct {
	class       string
	run         Runner
	scriptDir   string // dir holding <strategy>.sh
	activeLink  string // symlink the engine's init runs
	initService string // init script path to restart, e.g. /etc/init.d/nfqws
	dryRun      bool
	log         *slog.Logger
	route       SelectorSetter // non-nil => also point the service selector at "direct"
	external    bool           // true => strategy owned externally (zapretgen reconciler); Enable does routing only

	// alive reports whether the engine is actually running after a switch. Without
	// it a switch is fire-and-forget: the symlink moves, the init script returns 0
	// whether or not the engine survived, and `current` records a strategy nobody
	// confirmed. That is how a bad strategy becomes a dead engine plus a watchdog
	// restarting it forever. nil = unverified (the old behaviour).
	alive func(context.Context) bool

	mu      sync.Mutex
	current string // currently active strategy id (idempotency)
	lkg     string // last strategy id the engine was OBSERVED alive on
}

// VerifyWith supplies the liveness check a switch is confirmed against, turning
// a failed switch into a rollback to the last strategy the engine was seen
// running rather than into an outage.
func (s *ScriptSwitcher) VerifyWith(alive func(context.Context) bool) { s.alive = alive }

// RouteToDirect makes the switcher also point a service's per-service selector at
// "direct" on Enable, so the service's traffic flows out direct -> nfqws (where
// the engine desyncs it). Set this ONLY when the daemon owns a per-service
// config (the sel-<service> selectors exist); without it the switcher just swaps
// the global engine strategy and leaves routing alone.
func (s *ScriptSwitcher) RouteToDirect(sel SelectorSetter) { s.route = sel }

// YieldStrategy makes Enable do ONLY its routing-to-direct job and stop switching
// the engine strategy (no symlink, no restart). Set this when the zapretgen
// reconciler is armed and owns the nfqws strategy as the single writer (LOT-10b-arm)
// — otherwise the switcher and the reconciler would fight over the active symlink.
func (s *ScriptSwitcher) YieldStrategy() { s.external = true }

func newSwitcher(class string, run Runner, scriptDir, activeLink, initService string, dryRun bool, log *slog.Logger) *ScriptSwitcher {
	return &ScriptSwitcher{
		class: class, run: run, scriptDir: scriptDir, activeLink: activeLink,
		initService: initService, dryRun: dryRun, log: log,
	}
}

// NewZapret builds the nfqws (zapret) strategy switcher.
func NewZapret(run Runner, scriptDir, activeLink, initService string, dryRun bool, log *slog.Logger) *ScriptSwitcher {
	return newSwitcher(strategy.ClassZapret, run, scriptDir, activeLink, initService, dryRun, log)
}

// NewByeDPI builds the byedpi strategy switcher (alternative bypass engine).
func NewByeDPI(run Runner, scriptDir, activeLink, initService string, dryRun bool, log *slog.Logger) *ScriptSwitcher {
	return newSwitcher(strategy.ClassByeDPI, run, scriptDir, activeLink, initService, dryRun, log)
}

func (s *ScriptSwitcher) Class() string { return s.class }

func (s *ScriptSwitcher) Enable(ctx context.Context, service, strategyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// In a per-service config, ensure the service is routed direct -> nfqws every
	// time (it may be on a VPN pool from a prior chain step). SetSelector is
	// idempotent, so this is cheap even when already direct. Done before the
	// strategy-idempotency check so re-entering a zapret step still re-routes.
	if s.route != nil {
		selector := registry.SelectorTag(service)
		if s.dryRun {
			s.log.Info("dry-run: would route service direct -> nfqws", "service", service, "selector", selector)
		} else if err := s.route.SetSelector(ctx, selector, "direct"); err != nil {
			return err
		}
	}

	// Strategy owned externally (armed zapretgen reconciler is the single writer):
	// we have done the routing-to-direct above; the nfqws strategy itself is the
	// reconciler's job, so do not symlink/restart here.
	if s.external {
		return nil
	}

	if s.current == strategyID {
		return nil // already active: idempotent no-op, no restart
	}
	script := s.scriptDir + "/" + strategyID + ".sh"

	if s.dryRun {
		s.log.Info("dry-run: would switch bypass strategy",
			"engine", s.class, "service", service, "strategy", strategyID, "script", script, "from", s.current)
		s.current = strategyID
		return nil
	}

	if err := s.switchTo(ctx, script); err != nil {
		return err
	}
	if s.alive != nil && !s.alive(ctx) {
		// The engine did not come up on the new strategy. Put back the last one it
		// was seen alive on: an unapplied strategy costs one rung, a dead engine
		// costs the whole desync layer — silently, because nft's `flags bypass`
		// keeps traffic flowing undesynced.
		if s.lkg == "" {
			s.current = ""
			return fmt.Errorf("executor: %s did not start on %q and there is no known-good strategy to fall back to", s.class, strategyID)
		}
		back := s.scriptDir + "/" + s.lkg + ".sh"
		if err := s.switchTo(ctx, back); err != nil {
			s.current = ""
			return fmt.Errorf("executor: %s did not start on %q and the rollback to %q also failed: %w", s.class, strategyID, s.lkg, err)
		}
		s.current = s.lkg
		s.log.Warn("bypass strategy rejected, rolled back",
			"engine", s.class, "service", service, "refused", strategyID, "kept", s.lkg)
		return fmt.Errorf("executor: %s did not start on %q; kept %q", s.class, strategyID, s.lkg)
	}
	s.log.Info("switched bypass strategy",
		"engine", s.class, "service", service, "strategy", strategyID, "from", s.current)
	s.current = strategyID
	if s.alive != nil {
		s.lkg = strategyID // only ever set from an OBSERVED alive engine
	}
	return nil
}

func (s *ScriptSwitcher) switchTo(ctx context.Context, script string) error {
	if err := s.run.Run(ctx, "ln", "-sfn", script, s.activeLink); err != nil {
		return err
	}
	return s.run.Run(ctx, s.initService, "restart")
}

// VPN routes a service through a VPN pool by pointing the service's sing-box
// selector at that pool over the Clash API. On the R5S all tproxy traffic
// already flows through sing-box, so routing is a selector flip, not an nft
// change. Each service has its own selector (registry.SelectorTag), so services
// route independently. The strategy ID is the pool to select (e.g. vpn_url_test).
type VPN struct {
	clash    *dataplane.ClashClient
	dryRun   bool
	log      *slog.Logger
	bestNode func(service string) string // advisory: ranker's best node for a service ("" = use pool)
}

// NewVPN builds the vpn executor.
func NewVPN(clash *dataplane.ClashClient, dryRun bool, log *slog.Logger) *VPN {
	return &VPN{clash: clash, dryRun: dryRun, log: log}
}

// WithBestNode wires an advisory best-node lookup (e.g. noderank.Best). When it
// returns a node for the service, Enable points the selector at that concrete
// node instead of the pool url-test; an empty result (or a stale node the
// selector no longer lists) falls back to the pool. The ranker only advises —
// this executor is the single writer, so a VPN-node choice can never leak into
// a service Brain has placed on zapret/direct.
func (v *VPN) WithBestNode(fn func(service string) string) *VPN { v.bestNode = fn; return v }

func (v *VPN) Class() string { return strategy.ClassVPN }

// target is the selector value to set for a VPN step: the ranker's advised node
// when it has one, else the pool url-test. Pure (no I/O) so it is unit-testable.
func (v *VPN) target(service, pool string) string {
	if v.bestNode != nil {
		if n := v.bestNode(service); n != "" {
			return n
		}
	}
	return pool
}

// Enable re-asserts on every tick BY DESIGN, and must not grow a
// `if current == target { return nil }` short-circuit like ScriptSwitcher's.
//
// The reason that always holds is drift: the desync executor's applyNow writes
// "direct" into this same selector, and every sing-box respawn rebuilds the whole
// selector table, so something else moves it regularly and this call is what puts
// it back. The cost is ~11 requests per tick to 127.0.0.1, which is not worth
// trading a self-healing property for.
//
// The second reason holds wherever a ranker is wired: target is recomputed from
// bestNode, whose advice moves as it measures, so caching would freeze a service
// on the node it first got. Both roots wire it now — the client since 71bcded —
// and since the ranker gained a canary its advice moves on MEASURED capacity, not
// only on latency, which is exactly the advice that must not be cached.
func (v *VPN) Enable(ctx context.Context, service, strategyID string) error {
	selector := registry.SelectorTag(service)
	pool := strategyID // VPN strategy IDs name the pool to select
	target := v.target(service, pool)
	if v.dryRun {
		v.log.Info("dry-run: would point selector at vpn target",
			"service", service, "selector", selector, "pool", pool, "target", target)
		return nil
	}
	if target == pool {
		if err := v.clash.EnsurePool(ctx, pool); err != nil {
			return err
		}
	}
	if err := v.clash.SetSelector(ctx, selector, target); err != nil {
		if target == pool {
			return err
		}
		// Stale node advice (selector no longer lists it): fall back to the pool.
		v.log.Warn("vpn: best-node pin failed, falling back to pool",
			"service", service, "node", target, "pool", pool, "err", err.Error())
		if err := v.clash.EnsurePool(ctx, pool); err != nil {
			return err
		}
		return v.clash.SetSelector(ctx, selector, pool)
	}
	return nil
}

// Direct routes a service straight out by pointing its selector at "direct".
// Used for LOCKED (banks/gov) and as the fail-safe when all VPN pools are dead.
type Direct struct {
	clash  *dataplane.ClashClient
	dryRun bool
	log    *slog.Logger
}

// NewDirect builds the direct executor.
func NewDirect(clash *dataplane.ClashClient, dryRun bool, log *slog.Logger) *Direct {
	return &Direct{clash: clash, dryRun: dryRun, log: log}
}

func (d *Direct) Class() string { return strategy.ClassDirect }

func (d *Direct) Enable(ctx context.Context, service, _ string) error {
	selector := registry.SelectorTag(service)
	if d.dryRun {
		d.log.Info("dry-run: would point selector at direct", "service", service, "selector", selector)
		return nil
	}
	// A DirectOnly service (e.g. ru-direct) has no per-service selector — its
	// route rule already sends matches to the "direct" outbound — so a missing
	// selector is success, not a failure.
	if err := d.clash.SetSelector(ctx, selector, "direct"); err != nil && !errors.Is(err, dataplane.ErrSelectorNotFound) {
		return err
	}
	return nil
}
