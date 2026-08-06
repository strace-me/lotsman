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
	// stall reports that this service's CURRENT path is not carrying traffic, and
	// why, even though a header probe succeeds. nil = disabled.
	stall func(service string) (reason string, stalled bool)

	rotation map[string]int // service -> next lower position to silent-probe

	trigger chan string // on-demand "recheck now" requests, consumed on Run's goroutine
}

// SetStallOracle wires a per-service "is this path actually carrying traffic?"
// signal. A header-only probe answers a different question from the one that
// matters — it moves a couple of hundred bytes, so a path that establishes and
// then delivers nothing reads as healthy. Two independent sources see the real
// thing: the eye's frozen-flow detector (the TSPU IP-throttle, LOT-43) and the
// desktop client's desync canary (goodput below the floor).
//
// When set, an ACTIVE probe the prober reports healthy is overridden to a FAILURE,
// so Brain escalates off a path that is up but useless. The oracle returns the
// REASON as well, because the failure recorded against the service must say what
// was actually observed rather than borrow a neighbouring explanation.
//
// nil = disabled. Call before Run.
func (e *Engine) SetStallOracle(f func(service string) (reason string, stalled bool)) { e.stall = f }

// New builds an Engine. interval is the probe period (short for the demo,
// per-category minutes in production). obs and fails may be nil.
func New(bus *events.Bus, prober dataplane.Prober, pos Positioner, reg *registry.Registry, k *kb.KB, obs ProbeObserver, fails faillog.Recorder, interval time.Duration, log *slog.Logger) *Engine {
	if fails == nil {
		fails = faillog.Nop{}
	}
	return &Engine{
		bus: bus, prober: prober, pos: pos, reg: reg, kb: k, obs: obs, fails: fails,
		interval: interval, log: log, rotation: map[string]int{},
		trigger: make(chan string, 8),
	}
}

// ProbeNow requests an immediate probe of one service, bypassing the interval —
// the UI's "recheck now". Safe to call from any goroutine: the request is handed
// to Run's loop (which owns the probe path and the rotation map) and dropped if a
// recheck is already queued, so mashing the button cannot pile up work. The
// resulting verdict reaches Brain through the very same bus an interval probe uses.
func (e *Engine) ProbeNow(service string) {
	select {
	case e.trigger <- service:
	default: // a recheck is already queued — one is enough
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
		case name := <-e.trigger:
			// An on-demand recheck, run on THIS goroutine so it shares the rotation
			// map safely and feeds the brain through the same verdict bus as an
			// interval probe — no special path, no reassert conflict.
			e.probeService(ctx, name)
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
	if kind == "active" && v.OK && e.stall != nil {
		if reason, stalled := e.stall(service); stalled {
			v.OK = false
			v.Err = reason
		}
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
