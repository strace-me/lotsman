// Package zapretgen is the zapret config reconciler (LOT-10b): it composes the
// nfqws config for the services CURRENTLY on a zapret rung and (when armed)
// applies it as one unit. It is the single writer of the nfqws strategy, the
// nfqws-side analogue of pkg/reconcile (which owns the sing-box config).
//
// It is distinct from the pure helpers it drives: pkg/zaptune picks a recipe per
// service and pkg/nfqwsgen assembles the --new args; this package adds the
// STATEFUL orchestration — read which services are zapret-active (from Brain),
// compose, semantic-diff against the last config (churn-guard), and log/apply.
//
// Per-RULE, not per-service: a service contributes a --new block only while Brain
// holds it on a zapret step (services on VPN/direct, or with no zapret step,
// contribute nothing). nfqws is one process, so the whole config is composed from
// the full active set at once — hence a reconciler, not a per-service action.
//
// Without Arm it is PROPOSE-ONLY: composes, semantic-diffs, logs what it WOULD
// switch to, never touching the live nfqws strategy. Arm (gated behind a flag)
// makes it the SINGLE WRITER of the strategy — it writes/symlinks/restarts and
// canaries the switch, rolling back to the last-known-good config (and demoting
// the failed recipes in the KB) if an affected service degrades.
package zapretgen

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// Runner runs a command (executor.ExecRunner satisfies it); injected so the armed
// apply path is testable without touching the real nfqws engine.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// ArmConfig turns the reconciler from propose-only into the SINGLE WRITER of the
// nfqws strategy (LOT-10b-arm). Pass it to Arm. Probe verifies a service is
// reachable (the canary signal); Record folds a recipe's canary outcome into the
// KB (so a failed recipe is demoted and the next compose differs). LKGTarget is
// the active-symlink target at startup (the current working config, e.g. alt12.sh)
// — the floor we roll back to before any composed config has passed a canary.
type ArmConfig struct {
	Runner       Runner
	ComposedPath string                                        // where the composed <id>.sh is written
	ActiveLink   string                                        // symlink the nfqws init runs
	RestartCmd   []string                                      // e.g. ["/etc/init.d/nfqws","restart"]
	LKGTarget    string                                        // current active.sh target (rollback floor)
	Probe        func(ctx context.Context, target string) bool // service reachability probe
	Record       func(service, recipeID string, ok bool)       // kb outcome hook (nil = no learning)
	Settle       time.Duration                                 // wait after restart before the canary probes
	CanaryProbes int                                           // probe attempts per service (>=1)
}

// Reconciler composes the nfqws config from the zapret-active services and, when
// armed, applies it as the single writer with a canary + rollback-to-last-good.
// Construct via New (propose-only); call Arm to enable applying.
type Reconciler struct {
	services  []registry.Service
	position  func(service string) int // brain.Position: a service's current chain index
	recipes   []strategycat.Recipe
	pick      zaptune.Picker
	nfqwsPath string
	log       *slog.Logger

	armed     bool
	arm       ArmConfig
	lkgTarget string // symlink target of the last-known-good config (init = arm.LKGTarget)

	last string // last composed launcher text (semantic-diff: act only on change)
}

// Arm makes the reconciler the single writer of the nfqws strategy: a covered,
// changed composition is written, symlinked active, and the engine restarted,
// then canaried — a degraded affected service rolls back to the last-known-good
// (see ArmConfig). Without Arm the reconciler stays propose-only.
func (r *Reconciler) Arm(c ArmConfig) {
	r.armed = true
	r.arm = c
	r.lkgTarget = c.LKGTarget
}

// New builds a propose-only zapret reconciler. position is brain.Position (which
// chain step a service is on); recipes is the recipe catalog (strategycat.Load());
// pick chooses one recipe per service (zaptune.FirstPicker for the 10b seed).
func New(services []registry.Service, position func(string) int, recipes []strategycat.Recipe, pick zaptune.Picker, nfqwsPath string, log *slog.Logger) *Reconciler {
	return &Reconciler{services: services, position: position, recipes: recipes, pick: pick, nfqwsPath: nfqwsPath, log: log}
}

// Reconcile runs one pass: find the zapret-active services, compose their nfqws
// config, and (propose-only) log what it would switch to — but only when the
// composed config CHANGED since the last pass (churn-guard), so a steady state is
// silent. Safe to call on a ticker and on Brain transitions. Never touches the
// data plane in this slice.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	active := r.zapretActive()
	plan := zaptune.Compose(active, r.recipes, r.pick)

	if !plan.Covered {
		// Nothing to compose, or recipes don't cover every active service -> keep the
		// existing whole-config (alt12). Only worth a line when there ARE active
		// zapret services we couldn't fully cover.
		if len(active) > 0 {
			r.log.Info("zapret-compose: not applying — recipes don't cover all zapret-active services; keeping existing config",
				"active", serviceNames(active), "uncovered", plan.Uncovered, "chosen", plan.Chosen)
		}
		return nil
	}

	launcher, err := zapret.RenderComposed(plan.Args, r.nfqwsPath)
	if err != nil {
		return fmt.Errorf("zapret-compose: render: %w", err)
	}
	if launcher == r.last {
		return nil // unchanged since last pass: no-op (churn-guard)
	}
	// Record the attempt BEFORE applying so a canary-failed config is not re-applied
	// next tick — the KB demotion below changes the next compose, which changes the
	// launcher text, which is what re-arms evaluation (anti-oscillation).
	r.last = launcher

	if !r.armed {
		r.log.Info("zapret-compose: composed nfqws config CHANGED (PROPOSE-ONLY, not applied)",
			"services", serviceNames(active), "chosen", plan.Chosen, "blocks", len(active), "nfqws_args", len(plan.Args))
		return nil
	}
	return r.applyArmed(ctx, active, plan, launcher)
}

// zapretActive returns the services Brain currently holds on a zapret rung, in
// stable order. A service with no chain, or out of range, is skipped.
func (r *Reconciler) zapretActive() []registry.Service {
	var out []registry.Service
	for _, svc := range r.services {
		if len(svc.Chain) == 0 {
			continue
		}
		pos := r.position(svc.Name)
		if pos < 0 || pos >= len(svc.Chain) {
			continue
		}
		if svc.Chain[pos].StrategyClass == strategy.ClassZapret {
			out = append(out, svc)
		}
	}
	return out
}

// serviceNames returns the services' names in stable order (for deterministic logs).
func serviceNames(svcs []registry.Service) []string {
	out := make([]string, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}
