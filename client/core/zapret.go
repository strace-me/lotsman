package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/strace-me/lotsman/pkg/aggregate"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// zapretExec is the client's zapret-class StrategyExecutor. It keeps the router's
// contract — route the service DIRECT so its traffic reaches the local desync
// engine instead of disappearing into the tunnel — but instead of symlinking a
// launcher and restarting an init service, it recomposes the whole nfqws strategy
// and hands the argv to a managed nfqws process.
//
// Recomposition is WHOLE-config, never incremental: nfqws is one process carrying
// every service's --new block, and the StrategyExecutor interface only reports a
// service ENTERING a rung — never leaving it. Composing from remembered Enable
// calls would therefore keep desyncing services that moved to VPN long ago.
// desyncEngine is the slice of the nfqws engine the executor drives. It is an
// interface purely so the recompose/stop logic can be tested without a live
// NFQUEUE; *nfqws.Engine is the only implementation.
type desyncEngine interface {
	Apply(ctx context.Context, args []string) (restarted bool, err error)
	Stop(ctx context.Context) error
}

// desyncPlatformEngine is the engine as the CORE holds it: the executor's slice
// above, plus the measured liveness the status surface reads. It is an interface
// because there are now two implementations — nfqws behind an nft NFQUEUE on
// Linux, winws behind its own WinDivert filter on Windows — and the choice
// between them is made in exactly one place (Core.newZapretExec).
type desyncPlatformEngine interface {
	desyncEngine
	Alive() bool
}

type zapretExec struct {
	clash     *dataplane.ClashClient
	engine    desyncEngine
	recipes   []strategycat.Recipe
	pick      zaptune.Picker            // ranks candidate recipes (KB-learned, catalog order as tiebreak)
	files     string                    // zapret payload dir, for resolving .bin references
	hostlists string                    // dir for per-service hostlist files ("" = inline the domains)
	active    func() []registry.Service // services CURRENTLY on a zapret rung
	resolve   zaptune.Resolver
	// canary probes the service just after a strategy lands, and record folds that
	// verdict into the KB. Without them the picker cannot learn which recipe beats
	// the DPI in front of THIS machine.
	// life is the client's own lifetime. The verdict must not ride the applier's
	// per-call context (cancelled the moment Enable returns) nor escape
	// cancellation entirely — it has to die when the data plane does.
	life   func() context.Context
	canary func(ctx context.Context, service string) bool
	// notCarrying is the canary's standing verdict per service, read by the probing
	// engine through StallReason. Guarded separately from the executor's own lock:
	// the probe engine asks on its goroutine while a judge writes on another.
	carryMu     sync.Mutex
	notCarrying map[string]bool
	record      func(service, recipe string, ok bool)
	log         *slog.Logger

	// mu serialises the whole-config recomposition. Enable (a service entering the
	// rung) and Reconcile (the periodic sweep that stops the engine when the rung
	// empties) both recompose from active(), and they must not interleave.
	mu sync.Mutex

	// chosen maps each service on the rung to the desync recipe currently applied,
	// for the rich /status. It has its own tiny lock so a status read never blocks
	// behind mu while an Apply is restarting nfqws.
	chosenMu sync.Mutex
	chosen   map[string]string
}

func (z *zapretExec) Class() string { return strategy.ClassZapret }

// chosenRecipe returns the desync recipe currently applied for service (empty when
// none), for the rich /status. It uses chosenMu, not mu, so a status read never
// waits on an in-flight nfqws (re)start.
func (z *zapretExec) chosenRecipe(service string) string {
	z.chosenMu.Lock()
	defer z.chosenMu.Unlock()
	return z.chosen[service]
}

// setChosen replaces the per-service recipe map with an independent copy, so a
// later mutation of the plan cannot race a status read.
func (z *zapretExec) setChosen(chosen map[string]string) {
	cp := make(map[string]string, len(chosen))
	for k, v := range chosen {
		cp[k] = v
	}
	z.chosenMu.Lock()
	z.chosen = cp
	z.chosenMu.Unlock()
}

// Enable routes the service direct and re-applies the composed desync strategy.
func (z *zapretExec) Enable(ctx context.Context, service, _ string) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	// Route direct FIRST: the service may be sitting on a VPN pool from a previous
	// chain step, and desyncing traffic that never leaves through the local stack
	// does nothing. SetSelector is idempotent, so re-entering the rung is cheap.
	if err := z.clash.SetSelector(ctx, registry.SelectorTag(service), "direct"); err != nil {
		return err
	}
	plan, err := z.composeAndApplyLocked(ctx)
	if err != nil {
		return err
	}
	if plan == nil {
		return nil // nothing composed (rung empty or uncovered) — no verdict to take
	}
	// Judge asynchronously: Enable is on the applier's path and must not block it
	// for the settle window.
	jctx := ctx
	if z.life != nil {
		jctx = z.life()
	}
	go z.judge(jctx, service, plan.Chosen)
	return nil
}

// Reconcile recomposes the desync to match the CURRENT rung membership, stopping
// the engine when the rung has emptied. It is the periodic counterpart to Enable:
// Enable reacts to a service arriving, Reconcile catches the departures the
// executor interface never reports. It takes no verdict — nothing new was chosen.
func (z *zapretExec) Reconcile(ctx context.Context) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	_, err := z.composeAndApplyLocked(ctx)
	return err
}

// composeAndApplyLocked recomposes the whole nfqws strategy from the services
// currently on the rung and applies it, or stops the engine when none remain. It
// returns the applied plan (for the caller to judge), or nil when nothing was
// applied. Caller holds z.mu.
func (z *zapretExec) composeAndApplyLocked(ctx context.Context) (*zaptune.Plan, error) {
	active := z.active()
	if len(active) == 0 {
		// The last service left the zapret rung. Stop desyncing rather than keep
		// mangling traffic for services that have moved to VPN (LOT-46): the executor
		// interface only reports a service ENTERING a rung, so without this sweep the
		// engine would run its final --new blocks indefinitely. Stop is idempotent, so
		// a rung that is simply always empty costs a cheap no-op each tick.
		if err := z.engine.Stop(ctx); err != nil {
			return nil, fmt.Errorf("zapret: stop idle engine: %w", err)
		}
		z.setChosen(nil)
		return nil, nil
	}
	plan := zaptune.Compose(active, z.recipes, z.pick, z.resolve, z.hostlists)
	if !plan.Covered {
		// Composing a PARTIAL strategy is worse than composing none: a service whose
		// rule_set domains could not be resolved would be desynced for only its
		// inline domains, silently missing the rest (the discord-gateway regression).
		z.log.Warn("zapret: strategy not applied — some services are uncovered",
			"uncovered", plan.Uncovered)
		return nil, nil
	}
	// Write the hostlists BEFORE applying: nfqws must never be pointed at a file
	// that is not there yet. WriteIfChanged is the router's own primitive — atomic,
	// so nfqws cannot read a half-written list, and a no-op when the content is
	// identical, which matters because nfqws reloads on MTIME. Rewriting an
	// unchanged file would make it reload for nothing.
	changed := 0
	for path, domains := range plan.Hostlists {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("zapret: hostlist dir: %w", err)
		}
		body := []byte(strings.Join(domains, "\n") + "\n")
		wrote, err := aggregate.WriteIfChanged(path, body, 0o644)
		if err != nil {
			return nil, fmt.Errorf("zapret: write hostlist %s: %w", path, err)
		}
		if wrote {
			changed++
		}
	}
	// nfqws resolves a bare payload name against ITS working directory, which is
	// ours rather than the zapret installation's.
	full := absolutizePayloads(plan.Args, z.files)
	restarted, err := z.engine.Apply(ctx, full)
	if err != nil {
		return nil, err
	}
	if changed > 0 && z.hostlists != "" {
		if restarted {
			// The argv changed too (or the engine had to be relaunched), so the
			// reload-free claim would be false — say what actually happened.
			z.log.Info("zapret: hostlist membership updated (engine re-applied)", "files", changed)
		} else {
			// Apply reported no restart: nfqws picks the new membership up from the file
			// itself, so the engine kept running and no live connection lost its desync.
			z.log.Info("zapret: hostlist membership updated without restarting the engine",
				"files", changed)
		}
	}
	// Only announce a real change. Enable (on every reassert) and the periodic
	// Reconcile both recompose from active() each tick; when nothing changed, Apply
	// is a no-op (restarted=false), and logging "desync applied" every tick from two
	// sources is just noise. A genuine (re)apply — a new service, a recipe change —
	// still reports at INFO.
	if restarted {
		z.log.Info("zapret: desync applied", "services", len(active), "chosen", plan.Chosen)
	} else {
		z.log.Debug("zapret: desync unchanged", "services", len(active))
	}
	z.setChosen(plan.Chosen)
	return &plan, nil
}

// zapretServices reports the services whose CURRENT brain position sits on a
// zapret-class chain step — the exact set nfqws must carry blocks for.
func (c *Core) zapretServices() []registry.Service {
	if c.brain == nil {
		return nil
	}
	var out []registry.Service
	for _, st := range c.brain.Snapshot() {
		svc, ok := c.reg.Services[st.Service]
		if !ok {
			continue
		}
		for _, step := range svc.Chain {
			if step.Position == st.Position && step.StrategyClass == strategy.ClassZapret {
				out = append(out, svc)
				break
			}
		}
	}
	return out
}
