package remctl

import (
	"errors"
	"net"
	"testing"

	"github.com/strace-me/lotsman/pkg/incident"
	"github.com/strace-me/lotsman/pkg/misroute"
	"github.com/strace-me/lotsman/pkg/remediate"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// applyCall records one Apply invocation.
type applyCall struct {
	service string
	rung    int
	rem     singbox.Remediation
}

// fakeActions is an injectable Actions with recorded calls and optional errors.
type fakeActions struct {
	applies     []applyCall
	rollbacks   []string
	applyErr    error
	rollbackErr error
}

func (f *fakeActions) actions() Actions {
	return Actions{
		Apply: func(service string, rung int, rem singbox.Remediation) error {
			if f.applyErr != nil {
				return f.applyErr
			}
			f.applies = append(f.applies, applyCall{service, rung, rem})
			return nil
		},
		Rollback: func(service string) error {
			if f.rollbackErr != nil {
				return f.rollbackErr
			}
			f.rollbacks = append(f.rollbacks, service)
			return nil
		},
	}
}

// recCapture captures incidents in memory.
type recCapture struct{ recs []incident.Incident }

func (r *recCapture) Record(in incident.Incident) { r.recs = append(r.recs, in) }

func (r *recCapture) phases() []string {
	out := make([]string, len(r.recs))
	for i, in := range r.recs {
		out[i] = in.Phase
	}
	return out
}

func leak(svc string) misroute.Verdict {
	return misroute.Verdict{Service: svc, Misrouted: true, Kind: misroute.KindLeak, LeakRatio: 0.66}
}

func dead(svc string) misroute.Verdict {
	return misroute.Verdict{Service: svc, Misrouted: true, Kind: misroute.KindDead, DeadFlowRatio: 0.9}
}

func healthy(svc string) misroute.Verdict {
	return misroute.Verdict{Service: svc, Misrouted: false}
}

func noCIDRs() Inputs { return Inputs{CIDRs: map[string][]*net.IPNet{}} }

// (a) 1-2 misrouted passes do not act (hysteresis N=3).
func TestHysteresisBlocksBlip(t *testing.T) {
	fa := &fakeActions{}
	c := New(DefaultConfig(), fa.actions(), nil)

	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	if len(fa.applies) != 0 {
		t.Fatalf("acted after %d passes; want 0 (hysteresis N=3)", 2)
	}
	// A healthy pass resets the streak; two more misrouted must still not act.
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs())
	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	if len(fa.applies) != 0 {
		t.Fatalf("streak not reset by healthy pass; applies=%d, want 0", len(fa.applies))
	}
}

// (b) N consecutive misrouted passes apply exactly once, record applied, set rung.
func TestHysteresisAppliesOnNthPass(t *testing.T) {
	fa := &fakeActions{}
	rc := &recCapture{}
	c := New(DefaultConfig(), fa.actions(), rc)

	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	if len(fa.applies) != 1 {
		t.Fatalf("applies=%d, want exactly 1 at N=3", len(fa.applies))
	}
	ac := fa.applies[0]
	if ac.service != "youtube" || ac.rung != 2 || !ac.rem.RejectQUIC {
		t.Fatalf("apply mismatch: %+v (want youtube/rung2/reject-quic)", ac)
	}
	// detected + applied recorded.
	gotApplied := false
	for _, in := range rc.recs {
		if in.Phase == incident.PhaseApplied {
			gotApplied = true
		}
	}
	if !gotApplied {
		t.Errorf("no applied incident recorded; phases=%v", rc.phases())
	}
}

// rung 1 ip-fallback when CIDRs are learned, carrying the CIDRs to the remediation.
func TestApplyIPFallbackWithCIDRs(t *testing.T) {
	fa := &fakeActions{}
	c := New(DefaultConfig(), fa.actions(), nil)
	_, n, _ := net.ParseCIDR("142.251.0.0/16")
	in := Inputs{CIDRs: map[string][]*net.IPNet{"youtube": {n}}}

	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{leak("youtube")}, in)
	}
	if len(fa.applies) != 1 || fa.applies[0].rung != 1 {
		t.Fatalf("want one rung-1 apply, got %+v", fa.applies)
	}
	if len(fa.applies[0].rem.FallbackCIDRs) != 1 || fa.applies[0].rem.FallbackCIDRs[0] != "142.251.0.0/16" {
		t.Errorf("fallback CIDRs not carried: %+v", fa.applies[0].rem)
	}
}

// (c) after apply, health recovers within K: resolved + kept (no rollback).
func TestCanaryRecoversAndKeeps(t *testing.T) {
	fa := &fakeActions{}
	rc := &recCapture{}
	c := New(DefaultConfig(), fa.actions(), rc)

	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	// First canary pass: healthy → resolved, kept.
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs())

	if len(fa.rollbacks) != 0 {
		t.Fatalf("rolled back despite recovery: %v", fa.rollbacks)
	}
	resolved := false
	for _, in := range rc.recs {
		if in.Phase == incident.PhaseResolved {
			resolved = true
		}
	}
	if !resolved {
		t.Errorf("no resolved incident; phases=%v", rc.phases())
	}
	if c.st["youtube"].phase != phaseMonitoring {
		t.Errorf("phase=%v, want monitoring", c.st["youtube"].phase)
	}
}

// (d) after apply, no improvement within K: rollback + escalate to next rung.
func TestCanaryFailsRollsBackAndEscalates(t *testing.T) {
	fa := &fakeActions{}
	rc := &recCapture{}
	c := New(DefaultConfig(), fa.actions(), rc)

	// Confirm + apply rung 2 (dead → reject-quic).
	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	if fa.applies[0].rung != 2 {
		t.Fatalf("first apply rung=%d, want 2", fa.applies[0].rung)
	}
	// Canary window K=3 all still misrouted → rollback after the 3rd.
	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	if len(fa.rollbacks) != 1 {
		t.Fatalf("rollbacks=%d, want 1 after failed canary", len(fa.rollbacks))
	}
	rolled := false
	for _, in := range rc.recs {
		if in.Phase == incident.PhaseRolledBack {
			rolled = true
		}
	}
	if !rolled {
		t.Errorf("no rolled-back incident; phases=%v", rc.phases())
	}
	// Escalation: rung advanced to 3 for the next attempt. Confirm again.
	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	// rung 3 is terminal (node escalation) — no further Apply call, an escalated record.
	if len(fa.applies) != 1 {
		t.Errorf("applies=%d, want still 1 (rung 3 is not applied here)", len(fa.applies))
	}
	escalated := false
	for _, in := range rc.recs {
		if in.Phase == incident.PhaseEscalated {
			escalated = true
		}
	}
	if !escalated {
		t.Errorf("no escalated incident; phases=%v", rc.phases())
	}
}

// (e) healthy throughout: never acts.
func TestHealthyNeverActs(t *testing.T) {
	fa := &fakeActions{}
	c := New(DefaultConfig(), fa.actions(), nil)
	for i := 0; i < 10; i++ {
		c.Pass([]misroute.Verdict{healthy("youtube"), healthy("discord")}, noCIDRs())
	}
	if len(fa.applies) != 0 || len(fa.rollbacks) != 0 {
		t.Fatalf("acted on healthy services: applies=%d rollbacks=%d", len(fa.applies), len(fa.rollbacks))
	}
}

// Recovery: a kept remediation is removed after R sustained healthy passes.
func TestRecoveryRemovesAfterSustainedHealth(t *testing.T) {
	fa := &fakeActions{}
	c := New(Config{Hysteresis: 3, Canary: 3, RecoverAfter: 6}, fa.actions(), nil)

	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	// Sustained healthy: first pass → resolved/monitoring (healthyStreak=1), then
	// 5 more → streak 6 → remove.
	for i := 0; i < 6; i++ {
		c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs())
	}
	if len(fa.rollbacks) != 1 {
		t.Fatalf("recovery rollbacks=%d, want 1", len(fa.rollbacks))
	}
	if c.st["youtube"].phase != phaseIdle || c.st["youtube"].activeRung != 0 || c.st["youtube"].nextRung != 0 {
		t.Errorf("post-recovery state not reset: %+v", c.st["youtube"])
	}
}

// applyThenRecover drives a service from idle through apply (rung 2 / reject-quic)
// and then `healthy` healthy passes. It returns the rollback count observed right
// after the run. Hysteresis and canary are 1 here so apply+monitoring start fast.
// The first healthy pass takes canary→monitoring (healthyStreak=1), so to reach an
// effective window W the caller must pass `healthy` = W healthy passes total.
func applyThenRecover(t *testing.T, c *Controller, fa *fakeActions, svc string, healthyPasses int) {
	t.Helper()
	c.Pass([]misroute.Verdict{dead(svc)}, noCIDRs()) // hysteresis=1 → apply rung 2, phaseCanary
	for i := 0; i < healthyPasses; i++ {
		c.Pass([]misroute.Verdict{healthy(svc)}, noCIDRs())
	}
}

// LOT-26 (a): the FIRST recovery removes after the base RecoverAfter window.
func TestBackoffFirstRecoveryUsesBase(t *testing.T) {
	fa := &fakeActions{}
	c := New(Config{Hysteresis: 1, Canary: 1, RecoverAfter: 2, RecoverBackoffMax: 8}, fa.actions(), nil)

	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())    // apply, phaseCanary
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // canary→monitoring, streak=1
	if len(fa.rollbacks) != 0 {
		t.Fatalf("removed at streak 1; want base RecoverAfter=2, got rollbacks=%d", len(fa.rollbacks))
	}
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // streak=2 → remove
	if len(fa.rollbacks) != 1 {
		t.Fatalf("first recovery rollbacks=%d, want 1 at base window 2", len(fa.rollbacks))
	}
	if g := c.st["youtube"].recoverGen; g != 1 {
		t.Errorf("recoverGen=%d after first removal, want 1 (bumped for next retry)", g)
	}
}

// LOT-26 (b): a service that relapses after removal needs a LONGER healthy window
// before the next removal — backoff grows base(2) → 4 → cap.
func TestBackoffGrowsAcrossRelapses(t *testing.T) {
	fa := &fakeActions{}
	c := New(Config{Hysteresis: 1, Canary: 1, RecoverAfter: 2, RecoverBackoffMax: 8}, fa.actions(), nil)

	// gen 0: removes after 2 healthy passes.
	applyThenRecover(t, c, fa, "youtube", 2)
	if len(fa.rollbacks) != 1 {
		t.Fatalf("gen0 rollbacks=%d, want 1", len(fa.rollbacks))
	}

	// Relapse immediately, re-apply. gen is now 1 → effRecover=4. Two healthy passes
	// (the gen-0 window) must NOT remove yet.
	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())    // re-detect+apply (hysteresis=1)
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // →monitoring, streak=1
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // streak=2 (would remove at gen0)
	if len(fa.rollbacks) != 1 {
		t.Fatalf("gen1 removed too early at streak 2; rollbacks=%d, want still 1", len(fa.rollbacks))
	}
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // streak=3
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // streak=4 → remove
	if len(fa.rollbacks) != 2 {
		t.Fatalf("gen1 rollbacks=%d, want 2 at window 4", len(fa.rollbacks))
	}
	if g := c.st["youtube"].recoverGen; g != 2 {
		t.Errorf("recoverGen=%d after second removal, want 2", g)
	}
}

// LOT-26 (c): the generation resets to 0 after sustained health (a full
// RecoverBackoffMax idle-clean window with no re-apply) → next removal uses base.
func TestBackoffResetsAfterSustainedHealth(t *testing.T) {
	fa := &fakeActions{}
	c := New(Config{Hysteresis: 1, Canary: 1, RecoverAfter: 2, RecoverBackoffMax: 8}, fa.actions(), nil)

	applyThenRecover(t, c, fa, "youtube", 2) // first removal → recoverGen=1
	if c.st["youtube"].recoverGen != 1 {
		t.Fatalf("setup: recoverGen=%d, want 1", c.st["youtube"].recoverGen)
	}
	// Stay clean (idle, healthy, no re-apply) for the full RecoverBackoffMax window.
	for i := 0; i < 8; i++ {
		c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs())
	}
	if g := c.st["youtube"].recoverGen; g != 0 {
		t.Fatalf("recoverGen=%d after sustained health, want 0 (reset)", g)
	}
	// Next flap removes after the BASE window again (2), proving the reset.
	applyThenRecover(t, c, fa, "youtube", 2)
	if len(fa.rollbacks) != 2 {
		t.Fatalf("post-reset rollbacks=%d, want 2 at base window 2", len(fa.rollbacks))
	}
}

// LOT-26 (d): the backed-off window is capped at RecoverBackoffMax.
func TestBackoffRespectsCap(t *testing.T) {
	c := New(Config{Hysteresis: 1, Canary: 1, RecoverAfter: 2, RecoverBackoffMax: 8}, (&fakeActions{}).actions(), nil)
	s := &state{}

	cases := map[int]int{0: 2, 1: 4, 2: 8, 3: 8, 10: 8} // 2,4,8(cap),then stays 8
	for gen, want := range cases {
		s.recoverGen = gen
		if got := c.effRecover(s); got != want {
			t.Errorf("effRecover(gen=%d)=%d, want %d (cap=8)", gen, got, want)
		}
	}

	// Backoff disabled (RecoverBackoffMax=0) → always base regardless of gen.
	c2 := New(Config{Hysteresis: 1, Canary: 1, RecoverAfter: 2, RecoverBackoffMax: 0}, (&fakeActions{}).actions(), nil)
	s2 := &state{recoverGen: 5}
	if got := c2.effRecover(s2); got != 2 {
		t.Errorf("effRecover with backoff disabled=%d, want base 2", got)
	}
}

// Apply error: config unchanged, stays idle, retries (no rung advance).
func TestApplyErrorDoesNotAdvance(t *testing.T) {
	fa := &fakeActions{applyErr: errors.New("reconcile boom")}
	rc := &recCapture{}
	c := New(DefaultConfig(), fa.actions(), rc)

	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	if len(fa.applies) != 0 {
		t.Fatalf("apply recorded despite error: %d", len(fa.applies))
	}
	if c.st["youtube"].phase != phaseIdle {
		t.Errorf("phase=%v, want idle after apply error", c.st["youtube"].phase)
	}
	if c.st["youtube"].nextRung != 0 {
		t.Errorf("nextRung advanced on apply error: %d", c.st["youtube"].nextRung)
	}
}

// Monitoring relapse: an active remediation that stops holding rolls back + escalates.
func TestMonitoringRelapseRollsBack(t *testing.T) {
	fa := &fakeActions{}
	c := New(DefaultConfig(), fa.actions(), nil)

	for i := 0; i < 3; i++ {
		c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs())
	}
	c.Pass([]misroute.Verdict{healthy("youtube")}, noCIDRs()) // → monitoring
	if c.st["youtube"].phase != phaseMonitoring {
		t.Fatalf("setup: phase=%v, want monitoring", c.st["youtube"].phase)
	}
	c.Pass([]misroute.Verdict{dead("youtube")}, noCIDRs()) // relapse
	if len(fa.rollbacks) != 1 {
		t.Fatalf("relapse rollbacks=%d, want 1", len(fa.rollbacks))
	}
	if c.st["youtube"].nextRung != 3 {
		t.Errorf("nextRung=%d after relapse rollback, want 3", c.st["youtube"].nextRung)
	}
}

// remediationFor maps each action correctly.
func TestRemediationFor(t *testing.T) {
	_, n, _ := net.ParseCIDR("10.0.0.0/8")
	rq := remediationFor(remediate.Plan{Action: remediate.ActionRejectQUIC})
	if !rq.RejectQUIC || len(rq.FallbackCIDRs) != 0 {
		t.Errorf("reject-quic mapping wrong: %+v", rq)
	}
	ipf := remediationFor(remediate.Plan{Action: remediate.ActionIPFallback, CIDRs: []*net.IPNet{n}})
	if ipf.RejectQUIC || len(ipf.FallbackCIDRs) != 1 {
		t.Errorf("ip-fallback mapping wrong: %+v", ipf)
	}
}

// LOT-19: with a memory recommending reject-quic for the leak class, a fresh
// confirmed leak (whose planner default WITH CIDRs would be rung-1 ip-fallback)
// jumps straight to rung 2 (reject-quic) instead.
func TestMemoryJumpsToKnownRung(t *testing.T) {
	fa := &fakeActions{}
	mem := remediate.NewMemory()
	mem.Record("youtube", misroute.KindLeak, remediate.ActionRejectQUIC, true)
	mem.Record("youtube", misroute.KindLeak, remediate.ActionRejectQUIC, true)
	c := New(DefaultConfig(), fa.actions(), nil)
	c.SetMemory(mem)

	_, n, _ := net.ParseCIDR("142.251.0.0/16")
	in := Inputs{CIDRs: map[string][]*net.IPNet{"youtube": {n}}} // default would be rung 1

	for i := 0; i < 3; i++ { // N=3 hysteresis
		c.Pass([]misroute.Verdict{leak("youtube")}, in)
	}
	if len(fa.applies) != 1 {
		t.Fatalf("applies=%d, want 1", len(fa.applies))
	}
	if fa.applies[0].rung != 2 {
		t.Errorf("memory recommending reject-quic must jump to rung 2, got rung %d", fa.applies[0].rung)
	}
}

// LOT-19: a remediation whose canary resolves is recorded as working for that
// (service, failure-class), so a later Best() recommends it.
func TestMemoryRecordsResolvedOutcome(t *testing.T) {
	fa := &fakeActions{}
	mem := remediate.NewMemory()
	c := New(DefaultConfig(), fa.actions(), nil)
	c.SetMemory(mem)
	in := noCIDRs() // dead -> reject-quic (rung 2)

	for i := 0; i < 3; i++ { // apply after hysteresis
		c.Pass([]misroute.Verdict{dead("youtube")}, in)
	}
	c.Pass([]misroute.Verdict{healthy("youtube")}, in) // canary resolves

	if a, ok := mem.Best("youtube", misroute.KindDead); !ok || a != remediate.ActionRejectQUIC {
		t.Errorf("resolved reject-quic must be remembered, Best=(%q,%v)", a, ok)
	}
}
