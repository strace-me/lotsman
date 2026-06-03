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
