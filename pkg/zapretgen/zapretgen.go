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
// This slice is PROPOSE-ONLY: it composes, semantic-diffs, and logs what it WOULD
// switch to. It does not touch the live nfqws strategy (the existing executor
// still owns that) — arming it is a later, gated step once the executor-authority
// question is settled.
package zapretgen

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// Reconciler composes the nfqws config from the zapret-active services. In this
// propose-only slice it only logs what it would apply; construct via New.
type Reconciler struct {
	services  []registry.Service
	position  func(service string) int // brain.Position: a service's current chain index
	recipes   []strategycat.Recipe
	pick      zaptune.Picker
	nfqwsPath string
	log       *slog.Logger

	last string // last composed launcher text (semantic-diff: log only on change)
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
func (r *Reconciler) Reconcile(_ context.Context) error {
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
	r.last = launcher
	r.log.Info("zapret-compose: composed nfqws config CHANGED (PROPOSE-ONLY, not applied)",
		"services", serviceNames(active), "chosen", plan.Chosen, "blocks", len(active), "nfqws_args", len(plan.Args))
	return nil
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
