// Package remctl is the ARMED remediation controller (LOT-18b): the state
// machine that turns the propose-only planner (pkg/remediate) into a self-heal
// loop that actually mutates the live sing-box config — gated, with mandatory
// hysteresis, canary verification, and auto-rollback.
//
// This is the highest-stakes component in Lotsman: a wrong decision auto-edits
// the router config. The controller is therefore pure-ish and injectable — the
// "apply" and "rollback" actions and the misroute verdicts are fed in, so the
// whole state machine is unit-testable with fakes and a manual pass driver, no
// real reconcile required. main.go wires the real apply/rollback (which update
// an active-remediations map and call reconcile) behind a default-OFF flag.
//
// # The ladder (rungs, from docs/DESIGN-selfheal-misroute.md §4)
//
//	rung 1 — ip-fallback   : route learned CDN CIDRs to the service selector
//	rung 2 — reject-quic    : reject udp/443 so the client retries over TCP
//	rung 3 — escalate-node  : last resort
//
// A service climbs the ladder: it starts at the rung pkg/remediate.Decide
// proposes (1 or 2 from the verdict), and each failed canary advances it to the
// next rung on the next attempt. rung 3 is terminal — it is not canaried/rolled
// back here (node escalation is owned elsewhere); the controller just records it.
//
// # State machine (per service)
//
//	idle ──N consecutive misrouted passes (hysteresis)──▶ apply(rung) ──▶ canary
//	canary ──health recovers within K passes──▶ resolved ──▶ monitoring (active)
//	canary ──K passes elapse w/o recovery (or worse)──▶ rollback ──▶ idle (rung++)
//	monitoring ──sustained healthy (R passes)──▶ recover (remove) ──▶ idle
//	monitoring ──misrouted again──▶ (re-enter hysteresis, then apply next rung)
//
// Hysteresis N (default 3) blocks acting on a 1–2 pass blip — the live flap was
// intermittent (dead=0.33 transient). Canary K (default 3) bounds how long we
// trust an applied remediation before judging it. Recovery R (default 6) removes
// a remediation that has been unnecessary for a sustained window so rules do not
// pile up.
//
// # QUIC-retry backoff (LOT-26)
//
// reject-quic (rung 2) is a TEMPORARY measure: we want UDP/QUIC back as soon as
// the underlying flap clears. The recovery removal (above) is the optimistic
// "let's try QUIC again" event. But if QUIC immediately re-flaps, the naive
// remove-every-R-passes loop oscillates: remove → re-detect → re-apply →
// recover → remove …, a sing-box restart blip every few minutes.
//
// To damp this, the removal timing backs off exponentially PER SERVICE. Each
// removal that is followed by a relapse (the service re-applies a remediation
// before it has stayed clean long enough) bumps a retry generation g, and the
// effective recovery window grows: effRecover = min(RecoverAfter·2^g,
// RecoverBackoffMax). So a chronically flapping service stays on reject-quic
// (TCP) longer and longer between optimistic QUIC retries, instead of removing
// every R passes. A service that genuinely recovers — stays clean for a full
// RecoverBackoffMax-pass window after a removal with no re-apply — resets g to 0.
// Backoff changes ONLY the removal timing; the apply/canary/rollback/escalate
// ladder is unchanged.
package remctl

import (
	"net"

	"github.com/strace-me/lotsman/pkg/incident"
	"github.com/strace-me/lotsman/pkg/misroute"
	"github.com/strace-me/lotsman/pkg/remediate"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// phase is the per-service lifecycle stage inside the controller.
type phase int

const (
	phaseIdle       phase = iota // no remediation active; counting misrouted passes
	phaseCanary                  // remediation applied; verifying health for K passes
	phaseMonitoring              // canary passed; remediation kept, watching for recovery/relapse
)

// Config tunes the state machine. Use DefaultConfig and override as needed; a
// zero value disables hysteresis/canary (acts immediately, never rolls back),
// which is unsafe — always start from DefaultConfig.
type Config struct {
	// Hysteresis: act only after this many CONSECUTIVE misrouted passes (anti-blip).
	Hysteresis int
	// Canary: passes to verify an applied remediation before judging it.
	Canary int
	// RecoverAfter: BASE consecutive healthy passes while a remediation is active
	// before it is removed as unnecessary (0 = never auto-remove). With backoff
	// (below) this is the generation-0 window; later generations require more.
	RecoverAfter int
	// RecoverBackoffMax caps the backed-off recovery window (passes). The effective
	// removal threshold for retry generation g is min(RecoverAfter·2^g,
	// RecoverBackoffMax). It also defines the "genuinely recovered" window: after a
	// removal, staying clean (no re-apply) for this many passes resets g to 0.
	// 0 disables backoff (every removal uses RecoverAfter, like before).
	RecoverBackoffMax int
}

// DefaultConfig: N=3 hysteresis, K=3 canary, R=6 recover, backoff cap 96
// (= 6·2^4, ~16 passes per pass-minute caps the QUIC-retry wait around the tens
// of minutes for a chronically flapping service). See package doc.
func DefaultConfig() Config {
	return Config{Hysteresis: 3, Canary: 3, RecoverAfter: 6, RecoverBackoffMax: 96}
}

// Inputs supplies the per-service facts the embedded planner needs (learned
// CDN CIDRs for ip-fallback). It mirrors remediate.Inputs and is passed per
// pass so freshly learned CIDRs are picked up.
type Inputs = remediate.Inputs

// Actions are the side-effecting hooks the controller calls. They are injected
// so the state machine is testable with fakes. Both update the caller's active-
// remediations map and (in production) trigger a reconcile. A non-nil error
// means the live config was NOT changed; the controller treats the attempt as
// not-taken and will retry on a later pass (it does not advance the rung on an
// apply error, only on a failed canary).
type Actions struct {
	// Apply installs the remediation for service at the given rung and reconciles.
	Apply func(service string, rung int, rem singbox.Remediation) error
	// Rollback removes the service's remediation and reconciles back to clean.
	Rollback func(service string) error
}

// state is the controller's per-service memory.
type state struct {
	phase phase

	misroutedStreak int // consecutive misrouted passes (hysteresis counter, phaseIdle)
	healthyStreak   int // consecutive healthy passes while active (recovery counter)
	canaryLeft      int // passes remaining in the canary window (phaseCanary)

	activeRung  int              // rung currently applied (0 = none)
	nextRung    int              // rung to try on the NEXT apply (advances after a failed canary)
	lastVerdict misroute.Verdict // the verdict that drove the current attempt (for canary baseline + incident)

	// QUIC-retry backoff (LOT-26): recoverGen is the retry generation — how many
	// times this service's remediation has been removed-then-reapplied. It scales
	// the recovery removal window (effRecover). idleCleanStreak counts consecutive
	// healthy passes while idle AFTER a backed-off removal; reaching
	// RecoverBackoffMax means a genuine recovery and resets recoverGen to 0.
	recoverGen      int
	idleCleanStreak int
}

// Controller runs the armed ladder state machine across observe passes. It is
// not safe for concurrent use; drive it from a single goroutine (the observe
// loop). Construct with New.
type Controller struct {
	cfg     Config
	actions Actions
	rec     incident.Recorder
	st      map[string]*state
	mem     *remediate.Memory // optional remediation memory (LOT-19); nil = no recall/record
}

// SetMemory wires a remediation memory (LOT-19): the controller records each
// canary outcome (resolved/rolled-back) into it and, on a fresh remediation
// attempt, consults it to jump straight to a known-working rung instead of
// climbing from the planner's default. nil (the default) preserves prior behavior.
func (c *Controller) SetMemory(m *remediate.Memory) { c.mem = m }

// New builds a Controller. rec may be nil (incidents discarded). actions.Apply
// and actions.Rollback must be non-nil.
func New(cfg Config, actions Actions, rec incident.Recorder) *Controller {
	if rec == nil {
		rec = incident.Nop{}
	}
	if cfg.Hysteresis < 1 {
		cfg.Hysteresis = 1
	}
	if cfg.Canary < 1 {
		cfg.Canary = 1
	}
	return &Controller{cfg: cfg, actions: actions, rec: rec, st: map[string]*state{}}
}

// Pass feeds one observe pass: the verdicts for every service this pass, plus
// the planner Inputs (learned CIDRs). It advances each service's state machine
// and triggers Apply/Rollback as warranted. Verdicts not present for a service
// it is tracking are treated as "healthy" (no flows observed = not misrouted).
func (c *Controller) Pass(verdicts []misroute.Verdict, in Inputs) {
	seen := map[string]bool{}
	for _, v := range verdicts {
		seen[v.Service] = true
		c.step(v, in)
	}
	// A tracked service with an active remediation but no verdict this pass means
	// its flows vanished (often BECAUSE the remediation worked) — treat as healthy.
	for svc, s := range c.st {
		if seen[svc] {
			continue
		}
		if s.phase == phaseIdle && s.activeRung == 0 {
			continue
		}
		c.step(misroute.Verdict{Service: svc, Misrouted: false}, in)
	}
}

func (c *Controller) step(v misroute.Verdict, in Inputs) {
	s := c.st[v.Service]
	if s == nil {
		s = &state{phase: phaseIdle, nextRung: 0}
		c.st[v.Service] = s
	}

	switch s.phase {
	case phaseIdle:
		c.stepIdle(s, v, in)
	case phaseCanary:
		c.stepCanary(s, v, in)
	case phaseMonitoring:
		c.stepMonitoring(s, v, in)
	}
}

// stepIdle counts misrouted passes; on the N-th consecutive one it applies the
// planner's rung (or the escalated rung if a prior canary failed).
func (c *Controller) stepIdle(s *state, v misroute.Verdict, in Inputs) {
	if !v.Misrouted {
		s.misroutedStreak = 0
		// QUIC-retry backoff reset: if we are idle after a backed-off removal and
		// the service stays clean for a full RecoverBackoffMax window with no
		// re-apply, it genuinely recovered — drop the retry generation back to 0.
		if s.recoverGen > 0 && c.cfg.RecoverBackoffMax > 0 {
			s.idleCleanStreak++
			if s.idleCleanStreak >= c.cfg.RecoverBackoffMax {
				s.recoverGen = 0
				s.idleCleanStreak = 0
			}
		}
		return
	}
	s.misroutedStreak++
	if s.misroutedStreak < c.cfg.Hysteresis {
		return // anti-blip: not yet confirmed
	}

	// Confirmed. Pick the rung: the planner's choice, unless a prior failed canary
	// already pushed nextRung higher (escalation persists across attempts).
	plan := remediate.Decide(v, in)
	rung := plan.Rung
	// LOT-19: on a FRESH attempt (no escalation in progress) jump straight to a
	// remediation that has worked for this (service, failure-class) before, instead
	// of climbing from the planner's default rung. Skip a known ip-fallback when no
	// CIDRs are learned (it would be a no-op) and let the planner pick.
	knownGood := false
	if c.mem != nil && s.nextRung == 0 {
		if action, ok := c.mem.Best(v.Service, v.Kind); ok {
			r := rungForAction(action)
			if r == 1 && len(in.CIDRs[v.Service]) == 0 {
				r = 0
			}
			if r > 0 {
				rung = r
				plan = c.planForRung(v, in, rung)
				knownGood = true
			}
		}
	}
	if s.nextRung > rung {
		rung = s.nextRung
		plan = c.planForRung(v, in, rung)
		knownGood = false
	}
	s.lastVerdict = v
	detail := "misroute confirmed past hysteresis"
	if knownGood {
		detail = "misroute confirmed; jumping to known-working remediation (LOT-19 memory)"
	}
	c.record(v, plan, incident.PhaseDetected, detail)

	if rung >= 3 {
		// Node escalation is terminal here: record it, do not canary/rollback.
		c.record(v, plan, incident.PhaseEscalated, "rungs 1-2 exhausted; node escalation (owned elsewhere)")
		s.misroutedStreak = 0
		s.nextRung = 3
		return
	}

	rem := remediationFor(plan)
	if err := c.actions.Apply(v.Service, rung, rem); err != nil {
		// Apply failed → config unchanged. Stay idle, keep streak so the next
		// confirmed pass retries the same rung. Do NOT advance the rung.
		c.record(v, plan, incident.PhaseDetected, "apply failed (config unchanged): "+err.Error())
		return
	}
	s.activeRung = rung
	s.phase = phaseCanary
	s.canaryLeft = c.cfg.Canary
	s.misroutedStreak = 0
	s.healthyStreak = 0
	s.idleCleanStreak = 0 // re-applied before the reset window closed: backoff stands
	c.record(v, plan, incident.PhaseApplied, "remediation applied; canary verifying")
}

// stepCanary watches the applied remediation for K passes. Health recovered →
// resolved+monitoring. Window elapses without recovery → rollback + escalate.
func (c *Controller) stepCanary(s *state, v misroute.Verdict, in Inputs) {
	plan := c.planForRung(s.lastVerdict, in, s.activeRung)
	if !v.Misrouted {
		// Recovered within the canary window: keep the remediation, and remember
		// that this action resolved this service's failure-class (LOT-19).
		s.phase = phaseMonitoring
		s.healthyStreak = 1
		c.recordOutcome(s.lastVerdict, plan.Action, true)
		c.record(v, plan, incident.PhaseResolved, "health recovered within canary window; remediation kept")
		return
	}
	// Still misrouted. Spend a canary pass; if the window is exhausted, roll back.
	s.canaryLeft--
	if s.canaryLeft > 0 {
		return // give it more passes to settle
	}
	c.rollbackAndEscalate(s, v, in)
}

// stepMonitoring keeps the remediation; removes it after a sustained healthy
// window, or re-enters hysteresis if the service relapses.
func (c *Controller) stepMonitoring(s *state, v misroute.Verdict, in Inputs) {
	plan := c.planForRung(s.lastVerdict, in, s.activeRung)
	if v.Misrouted {
		// Relapse under the active remediation: the rung is not holding. Roll back
		// and escalate, same as a failed canary.
		s.healthyStreak = 0
		c.rollbackAndEscalate(s, v, in)
		return
	}
	s.healthyStreak++
	if c.cfg.RecoverAfter > 0 && s.healthyStreak >= c.effRecover(s) {
		if err := c.actions.Rollback(v.Service); err != nil {
			c.record(v, plan, incident.PhaseResolved, "recovery remove failed (remediation kept): "+err.Error())
			return
		}
		c.record(v, plan, incident.PhaseResolved, "sustained healthy; remediation removed (no longer needed)")
		s.phase = phaseIdle
		s.activeRung = 0
		s.nextRung = 0 // fully recovered → reset the ladder
		s.healthyStreak = 0
		s.misroutedStreak = 0
		// QUIC-retry backoff (LOT-26): this removal is an optimistic QUIC retry.
		// Bump the retry generation so that if QUIC re-flaps and we re-apply, the
		// NEXT removal requires a longer healthy window. The reset (stepIdle) drops
		// it back to 0 once the service stays clean for a full RecoverBackoffMax
		// window with no re-apply.
		if c.cfg.RecoverBackoffMax > 0 {
			s.recoverGen++
			s.idleCleanStreak = 0
		}
	}
}

// effRecover is the effective recovery-removal window (passes) for the service's
// current retry generation: min(RecoverAfter·2^gen, RecoverBackoffMax). With
// RecoverBackoffMax==0 (backoff disabled) it is just RecoverAfter.
func (c *Controller) effRecover(s *state) int {
	base := c.cfg.RecoverAfter
	if c.cfg.RecoverBackoffMax <= 0 || s.recoverGen == 0 {
		return base
	}
	// Compute base<<gen with overflow/cap guard (gen can grow unbounded otherwise).
	eff := base
	for i := 0; i < s.recoverGen; i++ {
		eff *= 2
		if eff >= c.cfg.RecoverBackoffMax {
			return c.cfg.RecoverBackoffMax
		}
	}
	return eff
}

// rollbackAndEscalate removes the current remediation and advances the rung so
// the next confirmed misroute tries a stronger one. Shared by failed-canary and
// monitoring-relapse paths.
func (c *Controller) rollbackAndEscalate(s *state, v misroute.Verdict, in Inputs) {
	plan := c.planForRung(s.lastVerdict, in, s.activeRung)
	if err := c.actions.Rollback(v.Service); err != nil {
		// Rollback failed: the config may still carry the (ineffective) remediation.
		// Do not advance — surface it and let the next pass retry the rollback.
		c.record(v, plan, incident.PhaseRolledBack, "rollback failed (config may still carry remediation): "+err.Error())
		return
	}
	// The applied action did not hold for this service's failure-class (LOT-19).
	c.recordOutcome(s.lastVerdict, plan.Action, false)
	c.record(v, plan, incident.PhaseRolledBack, "no recovery; rolled back to clean")
	s.nextRung = s.activeRung + 1
	s.activeRung = 0
	s.phase = phaseIdle
	s.misroutedStreak = 0
	s.canaryLeft = 0
}

// planForRung re-derives the plan for an explicit rung (escalation overrides the
// planner's verdict-based choice). Keeps incident records and the singbox
// remediation consistent regardless of how the rung was reached.
func (c *Controller) planForRung(v misroute.Verdict, in Inputs, rung int) remediate.Plan {
	switch rung {
	case 1:
		return remediate.Plan{
			Service: v.Service, Rung: 1, Action: remediate.ActionIPFallback,
			CIDRs:  in.CIDRs[v.Service],
			Reason: "ip-fallback (ladder rung 1)",
		}
	case 2:
		return remediate.Plan{
			Service: v.Service, Rung: 2, Action: remediate.ActionRejectQUIC,
			Reason: "reject-quic (ladder rung 2)",
		}
	case 3:
		return remediate.EscalatePlan(v.Service)
	default:
		return remediate.Decide(v, in)
	}
}

// recordOutcome folds an applied-remediation result into the memory, if wired
// (LOT-19). v carries the failure-class (s.lastVerdict.Kind) the action addressed.
func (c *Controller) recordOutcome(v misroute.Verdict, action string, ok bool) {
	if c.mem != nil {
		c.mem.Record(v.Service, v.Kind, action, ok)
	}
}

// rungForAction maps a remembered action back to its ladder rung (0 = unknown).
func rungForAction(action string) int {
	switch action {
	case remediate.ActionIPFallback:
		return 1
	case remediate.ActionRejectQUIC:
		return 2
	case remediate.ActionEscalateNode:
		return 3
	default:
		return 0
	}
}

// remediationFor maps a plan to the singbox.Remediation the reconciler applies.
func remediationFor(p remediate.Plan) singbox.Remediation {
	switch p.Action {
	case remediate.ActionIPFallback:
		cidrs := make([]string, 0, len(p.CIDRs))
		for _, n := range p.CIDRs {
			cidrs = append(cidrs, n.String())
		}
		return singbox.Remediation{FallbackCIDRs: cidrs}
	case remediate.ActionRejectQUIC:
		return singbox.Remediation{RejectQUIC: true}
	default:
		return singbox.Remediation{}
	}
}

func (c *Controller) record(v misroute.Verdict, p remediate.Plan, phase, note string) {
	c.rec.Record(incident.Incident{
		Service:       v.Service,
		Kind:          v.Kind,
		LeakRatio:     v.LeakRatio,
		DeadFlowRatio: v.DeadFlowRatio,
		Rung:          p.Rung,
		Action:        p.Action,
		CIDRs:         cidrStrings(p.CIDRs),
		Phase:         phase,
		Note:          note,
	})
}

func cidrStrings(cidrs []*net.IPNet) []string {
	if len(cidrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(cidrs))
	for _, n := range cidrs {
		out = append(out, n.String())
	}
	return out
}
