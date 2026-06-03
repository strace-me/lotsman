// Package applier converges production toward the desired state Brain emits.
// It owns no policy: it maps a desired (class, strategy) to an executor, runs
// it idempotently, then reports what is actually active back to Brain. The
// report closes the reconcile loop and lets Brain detect drift.
package applier

import (
	"context"
	"log/slog"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/executor"
)

// Applier wires desired-state events to executors.
type Applier struct {
	bus   *events.Bus
	execs map[string]executor.StrategyExecutor // class -> executor
	log   *slog.Logger
}

// New builds an Applier. execs are keyed by strategy class.
func New(bus *events.Bus, execs []executor.StrategyExecutor, log *slog.Logger) *Applier {
	m := make(map[string]executor.StrategyExecutor, len(execs))
	for _, e := range execs {
		m[e.Class()] = e
	}
	return &Applier{bus: bus, execs: m, log: log}
}

// Run consumes DesiredStateChanged until ctx is cancelled.
func (a *Applier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-a.bus.DesiredState:
			a.apply(ctx, d)
		}
	}
}

func (a *Applier) apply(ctx context.Context, d events.DesiredStateChanged) {
	ex, ok := a.execs[d.StrategyClass]
	if !ok {
		a.log.Error("no executor for class", "service", d.Service, "class", d.StrategyClass)
		return
	}
	if err := ex.Enable(ctx, d.Service, d.StrategyID); err != nil {
		// Apply failed: do NOT report actual=desired. Brain keeps seeing the
		// stale actual and will re-emit desired, so the next reconcile retries.
		a.log.Error("apply failed", "service", d.Service, "state", d.State, "err", err)
		return
	}
	a.log.Info("applied", "service", d.Service, "position", d.Position,
		"state", d.State, "class", d.StrategyClass, "strategy", d.StrategyID)

	// Read-back: M0 trusts a successful Enable as the actual state. Real
	// data-plane introspection (query nft/clash) replaces this on hardware.
	a.bus.ActualState <- events.ActualStateObserved{
		Service:  d.Service,
		Position: d.Position,
		State:    d.State,
	}
}
