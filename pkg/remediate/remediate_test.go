package remediate

import (
	"net"
	"testing"

	"github.com/strace-me/lotsman/pkg/iplearn"
	"github.com/strace-me/lotsman/pkg/misroute"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad cidr %q: %v", s, err)
	}
	return n
}

func TestPlanLeakWithCIDRs_RungOneIPFallback(t *testing.T) {
	v := misroute.Verdict{Service: "youtube", Misrouted: true, Kind: misroute.KindLeak, LeakRatio: 0.66}
	in := Inputs{CIDRs: map[string][]*net.IPNet{
		"youtube": {cidr(t, "142.251.0.0/16"), cidr(t, "172.217.0.0/16")},
	}}

	p := Decide(v, in)
	if p.Rung != 1 || p.Action != ActionIPFallback {
		t.Fatalf("rung=%d action=%q, want 1/%s", p.Rung, p.Action, ActionIPFallback)
	}
	if len(p.CIDRs) != 2 {
		t.Errorf("CIDRs=%d, want 2", len(p.CIDRs))
	}
	if p.Service != "youtube" {
		t.Errorf("service=%q", p.Service)
	}
	if p.Reason == "" {
		t.Error("empty reason")
	}
}

func TestPlanLeakNoCIDRs_RungTwoRejectQUIC(t *testing.T) {
	v := misroute.Verdict{Service: "youtube", Misrouted: true, Kind: misroute.KindLeak, LeakRatio: 0.66}

	p := Decide(v, Inputs{}) // no CIDRs learned yet
	if p.Rung != 2 || p.Action != ActionRejectQUIC {
		t.Fatalf("rung=%d action=%q, want 2/%s", p.Rung, p.Action, ActionRejectQUIC)
	}
	if len(p.CIDRs) != 0 {
		t.Errorf("CIDRs=%d, want 0", len(p.CIDRs))
	}
}

func TestPlanLeakEmptyCIDRSlice_RungTwoRejectQUIC(t *testing.T) {
	v := misroute.Verdict{Service: "youtube", Misrouted: true, Kind: misroute.KindLeak, LeakRatio: 0.5}
	in := Inputs{CIDRs: map[string][]*net.IPNet{"youtube": {}}} // present but empty

	p := Decide(v, in)
	if p.Rung != 2 || p.Action != ActionRejectQUIC {
		t.Fatalf("rung=%d action=%q, want 2/%s (empty slice = no CIDRs)", p.Rung, p.Action, ActionRejectQUIC)
	}
}

func TestPlanDead_RungTwoRejectQUIC(t *testing.T) {
	v := misroute.Verdict{Service: "discord", Misrouted: true, Kind: misroute.KindDead, DeadFlowRatio: 0.8}
	// Even with CIDRs available, a dead verdict goes to reject-quic (QUIC is the
	// problem; routing it by IP would not help).
	in := Inputs{CIDRs: map[string][]*net.IPNet{"discord": {cidr(t, "162.159.0.0/16")}}}

	p := Decide(v, in)
	if p.Rung != 2 || p.Action != ActionRejectQUIC {
		t.Fatalf("rung=%d action=%q, want 2/%s", p.Rung, p.Action, ActionRejectQUIC)
	}
}

// A stall is the TSPU IP-throttle: no route remediation can fix it (route toggles
// don't change the destination IP class). Decide must yield ActionNone so the
// remctl ladder doesn't loop reject-quic/escalate-node forever (LOT-43); the chain
// escalation (stall oracle → Brain) handles it instead.
func TestPlanStalled_NoRouteRemediation(t *testing.T) {
	v := misroute.Verdict{Service: "youtube", Misrouted: true, Kind: misroute.KindStalled, StalledRatio: 0.9}
	// Even with CIDRs available, a stall does NOT get reject-quic or ip-fallback.
	in := Inputs{CIDRs: map[string][]*net.IPNet{"youtube": {cidr(t, "142.250.0.0/15")}}}
	p := Decide(v, in)
	if p.Rung != 0 || p.Action != ActionNone {
		t.Fatalf("stalled rung=%d action=%q, want 0/%s (no route remediation)", p.Rung, p.Action, ActionNone)
	}
}

func TestPlanHealthy_None(t *testing.T) {
	v := misroute.Verdict{Service: "youtube", Misrouted: false, Kind: misroute.KindNone}

	p := Decide(v, Inputs{CIDRs: map[string][]*net.IPNet{"youtube": {cidr(t, "142.251.0.0/16")}}})
	if p.Rung != 0 || p.Action != ActionNone {
		t.Fatalf("rung=%d action=%q, want 0/%s", p.Rung, p.Action, ActionNone)
	}
}

func TestEscalatePlan_RungThreeReachable(t *testing.T) {
	p := EscalatePlan("youtube")
	if p.Rung != 3 || p.Action != ActionEscalateNode {
		t.Fatalf("rung=%d action=%q, want 3/%s", p.Rung, p.Action, ActionEscalateNode)
	}
	if p.Service != "youtube" || p.Reason == "" {
		t.Errorf("service=%q reason=%q", p.Service, p.Reason)
	}
}

func TestInputsFromLearner(t *testing.T) {
	l := iplearn.NewLearner()
	l.Observe("youtube", net.ParseIP("142.251.1.1"))
	l.Observe("youtube", net.ParseIP("142.251.1.2"))

	in := InputsFromLearner(l, []string{"youtube", "discord"})
	if len(in.CIDRs["youtube"]) == 0 {
		t.Error("youtube CIDRs not carried from learner")
	}
	if _, ok := in.CIDRs["discord"]; ok {
		t.Error("discord has no learned CIDRs; should be absent")
	}

	// A nil learner yields an empty (non-nil) map so Plan can index it safely.
	in2 := InputsFromLearner(nil, []string{"youtube"})
	if in2.CIDRs == nil {
		t.Error("nil learner should still give a non-nil CIDR map")
	}
	if got := Decide(misroute.Verdict{Service: "youtube", Misrouted: true, Kind: misroute.KindLeak}, in2); got.Action != ActionRejectQUIC {
		t.Errorf("with nil learner, leak should fall to reject-quic, got %q", got.Action)
	}
}
