// Package applier converges production toward the desired state Brain emits.
// It owns no policy: it maps a desired (class, strategy) to an executor, runs
// it idempotently, then reports what is actually active back to Brain. The
// report closes the reconcile loop and lets Brain detect drift.
package applier

import (
	"context"
	"log/slog"
	"sync"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/executor"
)

// Applier wires desired-state events to executors.
type Applier struct {
	bus   *events.Bus
	execs map[string]executor.StrategyExecutor // class -> executor
	log   *slog.Logger

	// Actual-state reports are forwarded to the bus by a dedicated goroutine,
	// decoupled from the read loop (LOT-37): the previous inline blocking send to
	// bus.ActualState could, with Brain blocking on its DesiredState send (under a
	// full buffer during flap), cyclically deadlock the whole control loop. The
	// read loop now never blocks on ActualState — it stashes the latest report per
	// service (coalesced: Brain only cares about the newest actual position) and
	// nudges the forwarder, which owns all ActualState sends.
	pendMu sync.Mutex
	pend   map[string]events.ActualStateObserved
	signal chan struct{}
}

// New builds an Applier. execs are keyed by strategy class.
func New(bus *events.Bus, execs []executor.StrategyExecutor, log *slog.Logger) *Applier {
	m := make(map[string]executor.StrategyExecutor, len(execs))
	for _, e := range execs {
		m[e.Class()] = e
	}
	return &Applier{
		bus: bus, execs: m, log: log,
		pend:   map[string]events.ActualStateObserved{},
		signal: make(chan struct{}, 1),
	}
}

// Run consumes DesiredStateChanged until ctx is cancelled. A sibling goroutine
// forwards actual-state reports so a saturated ActualState channel never stalls
// this read loop (the LOT-37 deadlock break).
func (a *Applier) Run(ctx context.Context) {
	go a.forwardActuals(ctx)
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
	a.reportActual(events.ActualStateObserved{
		Service:  d.Service,
		Position: d.Position,
		State:    d.State,
	})
}

// reportActual stashes the latest actual state for a service and nudges the
// forwarder. Never blocks the caller (the read loop): the map write is bounded
// by service count and the signal send is non-blocking.
func (a *Applier) reportActual(ev events.ActualStateObserved) {
	a.pendMu.Lock()
	a.pend[ev.Service] = ev
	a.pendMu.Unlock()
	select {
	case a.signal <- struct{}{}:
	default: // a wakeup is already pending; the forwarder will see this entry
	}
}

// forwardActuals owns every send to bus.ActualState. It drains the coalesced
// pending map on each nudge; a blocking send here cannot wedge the Applier's read
// loop (that runs in Run's goroutine), so Brain always gets drained and the
// Brain<->Applier cycle can't deadlock (LOT-37).
func (a *Applier) forwardActuals(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.signal:
		}
		for _, ev := range a.drainPending() {
			select {
			case a.bus.ActualState <- ev:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (a *Applier) drainPending() []events.ActualStateObserved {
	a.pendMu.Lock()
	defer a.pendMu.Unlock()
	if len(a.pend) == 0 {
		return nil
	}
	out := make([]events.ActualStateObserved, 0, len(a.pend))
	for k, ev := range a.pend {
		out = append(out, ev)
		delete(a.pend, k)
	}
	return out
}
