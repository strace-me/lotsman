// Package probing runs active probes on a per-service interval and publishes
// verdicts. It also records each outcome into the KB (the feedback loop's
// KB[EWMA] arm). It holds no policy: it asks the Positioner where each service
// currently sits, probes that position, and — when the service has escalated —
// rotates a silent probe through a lower position so Brain can detect recovery.
package probing

import (
	"context"
	"log/slog"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/faillog"
	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/registry"
)

// Positioner reports a service's current chain position. Brain implements it.
type Positioner interface {
	Position(service string) int
}

// ProbeObserver is notified of every probe outcome (e.g. the metrics collector).
type ProbeObserver interface {
	ObserveProbe(service string, ok bool, rttMs int)
}

// Engine drives probing for all registered services.
type Engine struct {
	bus      *events.Bus
	prober   dataplane.Prober
	pos      Positioner
	reg      *registry.Registry
	kb       *kb.KB
	obs      ProbeObserver
	fails    faillog.Recorder
	interval time.Duration
	log      *slog.Logger
	stall    func(service string) bool // LOT-43: is this service throttle-stalled right now? nil = disabled

	rotation map[string]int // service -> next lower position to silent-probe
}

// SetStallOracle wires a per-service "is this service throttle-stalled?" signal
// (from the eye/misroute snapshot). A header-only probe cannot see the TSPU
// IP-throttle freeze, but the eye can (frozen flows). When set, an ACTIVE probe
// the prober reports healthy is overridden to a FAILURE if the service is stalled,
// so Brain escalates off the throttled path to a foreign egress — the only thing
// that escapes an IP-keyed throttle (LOT-43). nil = disabled. Call before Run.
func (e *Engine) SetStallOracle(f func(service string) bool) { e.stall = f }

// New builds an Engine. interval is the probe period (short for the demo,
// per-category minutes in production). obs and fails may be nil.
func New(bus *events.Bus, prober dataplane.Prober, pos Positioner, reg *registry.Registry, k *kb.KB, obs ProbeObserver, fails faillog.Recorder, interval time.Duration, log *slog.Logger) *Engine {
	if fails == nil {
		fails = faillog.Nop{}
	}
	return &Engine{
		bus: bus, prober: prober, pos: pos, reg: reg, kb: k, obs: obs, fails: fails,
		interval: interval, log: log, rotation: map[string]int{},
	}
}

// Run probes every service each interval until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for name := range e.reg.Services {
				e.probeService(ctx, name)
			}
		}
	}
}

func (e *Engine) probeService(ctx context.Context, service string) {
	// A service with no probe target cannot be actively probed (e.g. a LOCKED
	// direct service like ru_direct, which is always direct and never fails over)
	// — skip it rather than issue a GET "" that always errors.
	if svc, ok := e.reg.Services[service]; ok && svc.ProbeTarget == "" {
		return
	}
	pos := e.pos.Position(service)

	// Active probe: liveness of the currently active strategy.
	e.runProbe(ctx, service, pos, "active")

	// Silent recovery probe: rotate through a lower (preferred) position so
	// Brain can detect when a preferred strategy is healthy again.
	if pos > 0 {
		lower := e.rotation[service] % pos
		e.rotation[service]++
		e.runProbe(ctx, service, lower, "silent")
	}
}

func (e *Engine) runProbe(ctx context.Context, service string, position int, kind string) {
	v := e.prober.Probe(ctx, service, position)

	// LOT-43: the prober is a header-only reachability check — blind to the TSPU
	// IP-throttle freeze (a connection establishes, moves a few KB, then silently
	// hangs). The eye sees it as frozen flows. Override a "healthy" ACTIVE probe to
	// a failure when the service is throttle-stalled so Brain escalates off the
	// throttled path; only a foreign egress escapes an IP-keyed throttle.
	if kind == "active" && v.OK && e.stall != nil && e.stall(service) {
		v.OK = false
		v.Err = "throttle-stall: flows frozen mid-stream (TSPU IP-throttle); escalate to a foreign egress (LOT-43)"
	}

	// Feedback: record the outcome in the KB (EWMA). Keyed by the step's
	// strategy when statically known, else by state name (M0; ALT_ZAPRET's
	// strategy is KB-resolved at runtime and not load-bearing for ranking yet).
	strat := e.strategyKey(service, position)
	ewma := e.kb.RecordOutcome(service, strat, v.OK, v.RTTms)

	if e.obs != nil {
		e.obs.ObserveProbe(service, v.OK, v.RTTms)
	}

	e.log.Info("probe",
		"service", service, "kind", kind, "position", position, "strategy", strat,
		"ok", v.OK, "rtt_ms", v.RTTms, "err", v.Err, "ewma", round2(ewma))

	if !v.OK {
		e.fails.Record(faillog.Failure{
			Service: service, Position: position, Kind: kind,
			StrategyID: strat, RTTms: v.RTTms, Err: v.Err,
		})
	}

	// Publish to Brain.
	select {
	case e.bus.Verdicts <- v:
	case <-ctx.Done():
	}
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func (e *Engine) strategyKey(service string, position int) string {
	svc, ok := e.reg.Services[service]
	if !ok || position >= len(svc.Chain) {
		return "unknown"
	}
	step := svc.Chain[position]
	if step.StrategyID != "" {
		return step.StrategyID
	}
	return step.State
}
