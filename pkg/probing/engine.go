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
	"github.com/strace-me/lotsman/pkg/strategy"
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
	// why, even though a header probe succeeds. measured distinguishes a verdict
	// from bytes counted off the wire from one inferred over a ratio. nil = disabled.
	stall func(service string) (reason string, stalled, measured bool)
	// carrying reports that the service is demonstrably moving real traffic right
	// now, from PASSIVE observation of the user's own connections. nil = disabled.
	carrying func(service string) (reason string, ok bool)

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
// It also returns MEASURED, and that third value is the whole difference between
// the two sources. The canary counted bytes off the wire: "this rule's own volume
// target delivered nothing" is a fact. The eye counted a ratio: "five of seven
// flows look frozen" is a suspicion, and a video player's abandoned parallel range
// requests look exactly like frozen flows. Only the fact outranks the activity
// veto below; the suspicion loses to megabytes the user is visibly moving.
//
// Both were filed under one flag until 2026-08-09, and the router paid for it: the
// eye walked YouTube to VPN three times in ten minutes while the owner watched an
// uninterrupted video, because a suspicion was given a measurement's authority.
//
// nil = disabled. Call before Run.
func (e *Engine) SetStallOracle(f func(service string) (reason string, stalled, measured bool)) {
	e.stall = f
}

// SetActivityOracle wires the OTHER half of the same problem. A synthetic probe
// fetches one URL over one protocol, so it can be wrong in both directions: it
// says healthy over a path carrying nothing (that is what SetStallOracle fixes),
// and it says broken over a path the user is visibly using right now.
//
// Both were measured on the same machine within a day. youtube passed a 204 fetch
// while carrying no video; Discord's probe timed out on its gateway URL for
// minutes while 3.5 MB of voice flowed through the same rule — raw UDP the probe
// never touches. Escalating on the second would have torn down a working call to
// chase a failure that existed only in the probe.
//
// So a FAILED active probe is vetoed when passive observation of the user's own
// connections shows the service genuinely moving traffic. A probe failed by a
// MEASURED stall is not vetoed — bytes counted beat bytes inferred — but a probe
// failed by an inferred one is, because the veto is itself a measurement.
//
// nil = disabled. Call before Run.
func (e *Engine) SetActivityOracle(f func(service string) (reason string, ok bool)) { e.carrying = f }

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

	// The prober could not reach the rung it was asked about, so it has nothing to
	// say about it. Recording either outcome would be a verdict about something the
	// measurement did not touch — the failure this project keeps finding — and
	// publishing one would move the brain on it. Say so and stop.
	if v.Unmeasured {
		e.log.Info("probe skipped: the rung could not be measured from here",
			"service", service, "kind", kind, "position", position, "why", v.Err)
		return
	}

	// LOT-43: the prober is a header-only reachability check — blind to the TSPU
	// IP-throttle freeze (a connection establishes, moves a few KB, then silently
	// hangs). The eye sees it as frozen flows. Override a "healthy" ACTIVE probe to
	// a failure when the service is throttle-stalled so Brain escalates off the
	// throttled path; only a foreign egress escapes an IP-keyed throttle.
	// byMeasurement records that the failure came from the stall oracle rather than
	// from the fetch. It outranks the activity veto below: a measured "carries
	// nothing" must not be undone by "some traffic is moving".
	byMeasurement := false
	// The verdict is about this rule's DESYNC, so it must apply to a silent probe of
	// a desync rung too. Applying it only to the active probe created a loop: the
	// active probe failed on the measurement while the silent probe of the rung
	// below passed on its own, the brain recovered, the active probe failed again —
	// and every transition resets the failure counter, so the rule ping-ponged
	// forever with fails=0 and the verdict stayed green over a service that was
	// carrying nothing. Measured on the ThinkPad: 54 such failures in six minutes
	// while YouTube did not load and the app said 11/11.
	if v.OK && e.stall != nil && (kind == "active" || e.isZapretRung(service, position)) {
		if reason, stalled, measured := e.stall(service); stalled {
			// byMeasurement carries the oracle's OWN answer now, instead of assuming
			// every stall verdict was measured. An inferred stall still fails the probe
			// — the eye is usually right — but it no longer silences the one signal that
			// can contradict it.
			v.OK, v.Err, byMeasurement = false, reason, measured
		}
	}
	// The mirror case, and the one that cost a working voice call: the probe failed
	// on its own while the user's OWN traffic shows the service working. One URL
	// over one protocol is a proxy for the service's health, and when the real
	// thing is observable the proxy does not get to overrule it.
	if kind == "active" && !v.OK && !byMeasurement && e.carrying != nil {
		if reason, ok := e.carrying(service); ok {
			e.log.Info("probe failed but the service is visibly carrying traffic — not counting it against the rung",
				"service", service, "probe_err", v.Err, "evidence", reason)
			v.OK, v.Err = true, ""
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

// isZapretRung reports whether the given chain position is a desync rung, so the
// canary's verdict about the desync reaches every probe of that path rather than
// only the active one.
func (e *Engine) isZapretRung(service string, position int) bool {
	svc, ok := e.reg.Services[service]
	if !ok || position < 0 || position >= len(svc.Chain) {
		return false
	}
	return svc.Chain[position].StrategyClass == strategy.ClassZapret
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
