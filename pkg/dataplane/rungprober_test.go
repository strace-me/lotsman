package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// fakeBase records whether the wrapped prober was consulted.
type fakeBase struct {
	called  bool
	verdict events.ProductionVerdict
}

func (f *fakeBase) Probe(context.Context, string, int) events.ProductionVerdict {
	f.called = true
	return f.verdict
}

// rungReg is a one-service registry: rung 0 is a VPN pool, rung 1 is direct.
func rungReg() *registry.Registry {
	return &registry.Registry{Services: map[string]registry.Service{
		"web": {
			Name: "web",
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_pool"},
				{Position: 1, State: registry.StateLocked, StrategyClass: strategy.ClassDirect, StrategyID: "direct"},
			},
		},
	}}
}

// clashStub serves the two endpoints the prober uses: the selector's current
// target, and the pool delay test.
func clashStub(t *testing.T, now string, delay int, delayErr bool) *ClashClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/delay"):
			if delayErr {
				w.WriteHeader(http.StatusGatewayTimeout)
				json.NewEncoder(w).Encode(map[string]any{"message": "An error occurred in the delay test"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"delay": delay})
		default:
			json.NewEncoder(w).Encode(map[string]any{"type": "Selector", "now": now})
		}
	}))
	t.Cleanup(srv.Close)
	return NewClashClient(srv.URL, "")
}

func TestRungProberDelayTestsAnInactiveVPNRung(t *testing.T) {
	// Selector sits on "direct", so rung 0 (vpn_pool) is INACTIVE: routing a probe
	// through the selector would measure direct and credit the VPN pool with its
	// health. The delay test must be used instead.
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 26}}
	p := NewRungProber(base, clashStub(t, "direct", 42, false), rungReg(), "", 0)

	got := p.Probe(context.Background(), "web", 0)
	if base.called {
		t.Error("base prober must NOT be consulted for an inactive vpn rung")
	}
	if !got.OK || got.RTTms != 42 {
		t.Errorf("verdict = %+v, want OK with the delay-test RTT 42", got)
	}
}

func TestRungProberDelegatesWhenTheRungIsActive(t *testing.T) {
	// Selector is on vpn_pool, so rung 0 IS live and the ordinary path probe
	// measures it truthfully — and against the service's own target.
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 26}}
	p := NewRungProber(base, clashStub(t, "vpn_pool", 42, false), rungReg(), "", 0)

	got := p.Probe(context.Background(), "web", 0)
	if !base.called {
		t.Error("an active rung must be probed through the normal path")
	}
	if got.RTTms != 26 {
		t.Errorf("verdict = %+v, want the base prober's", got)
	}
}

func TestRungProberDelegatesForNonVPNRungs(t *testing.T) {
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 7}}
	p := NewRungProber(base, clashStub(t, "direct", 42, false), rungReg(), "", 0)

	if got := p.Probe(context.Background(), "web", 1); !base.called || got.RTTms != 7 {
		t.Errorf("direct rung must delegate; called=%v verdict=%+v", base.called, got)
	}
}

func TestRungProberReportsADeadPool(t *testing.T) {
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 26}}
	p := NewRungProber(base, clashStub(t, "direct", 0, true), rungReg(), "", 0)

	got := p.Probe(context.Background(), "web", 0)
	if got.OK {
		t.Errorf("a failed delay test must be a failed verdict, got %+v", got)
	}
	if got.Err == "" {
		t.Error("the failure reason must be carried in the verdict")
	}
}

func TestRungProberClampsTimeoutUnderTheClientBudget(t *testing.T) {
	// Asking for a delay test at least as long as the Clash client's own HTTP
	// timeout makes the client abandon the request before sing-box can answer,
	// which turns a slow-but-healthy pool into a spurious failure.
	c := clashStub(t, "direct", 42, false)
	c.Client.Timeout = 3 * time.Second

	for _, requested := range []time.Duration{0, 5 * time.Second, 3 * time.Second} {
		p := NewRungProber(&fakeBase{}, c, rungReg(), "", requested)
		if p.timeout >= c.Client.Timeout {
			t.Errorf("requested %s: timeout %s must stay under the client budget %s",
				requested, p.timeout, c.Client.Timeout)
		}
	}
	// A comfortably short request is honoured as-is.
	if p := NewRungProber(&fakeBase{}, c, rungReg(), "", time.Second); p.timeout != time.Second {
		t.Errorf("a short timeout must be kept, got %s", p.timeout)
	}
}

// fakeDirect stands in for the direct-dialing prober.
type fakeDirect struct {
	called  bool
	verdict events.ProductionVerdict
}

func (f *fakeDirect) Probe(context.Context, string, int) events.ProductionVerdict {
	f.called = true
	return f.verdict
}

func TestRungProberProbesAnInactiveDirectRungDirect(t *testing.T) {
	// Service sits on the VPN node (selector = vpn_pool). Silent-probing the direct
	// rung (pos 1) through the selector would measure the VPN node and credit the
	// direct rung with its health — the LOT-44 mis-credit. With a direct prober set,
	// it must dial direct instead.
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 26}}
	direct := &fakeDirect{verdict: events.ProductionVerdict{OK: true, RTTms: 9}}
	p := NewRungProber(base, clashStub(t, "vpn_pool", 42, false), rungReg(), "", 0)
	p.SetDirectProber(direct)

	got := p.Probe(context.Background(), "web", 1)
	if base.called {
		t.Error("an inactive direct rung must NOT be probed through the selector")
	}
	if !direct.called || got.RTTms != 9 {
		t.Errorf("expected the direct prober's verdict; called=%v verdict=%+v", direct.called, got)
	}
}

func TestRungProberUsesBaseForADirectRungWhenAlreadyDirect(t *testing.T) {
	// Selector already on "direct": the ordinary path measures the direct route, so
	// the special direct prober must NOT be diverted to.
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 7}}
	direct := &fakeDirect{verdict: events.ProductionVerdict{OK: true, RTTms: 9}}
	p := NewRungProber(base, clashStub(t, "direct", 42, false), rungReg(), "", 0)
	p.SetDirectProber(direct)

	got := p.Probe(context.Background(), "web", 1)
	if direct.called {
		t.Error("with the selector already on direct, base must measure it — no divert")
	}
	if !base.called || got.RTTms != 7 {
		t.Errorf("expected the base verdict; base.called=%v verdict=%+v", base.called, got)
	}
}

func TestRungProberVPNRungStillWinsOverDirectProber(t *testing.T) {
	// Even with a direct prober set, an inactive VPN rung must take the delay-test
	// path, not the direct prober.
	direct := &fakeDirect{verdict: events.ProductionVerdict{OK: true, RTTms: 9}}
	p := NewRungProber(&fakeBase{}, clashStub(t, "direct", 42, false), rungReg(), "", 0)
	p.SetDirectProber(direct)

	got := p.Probe(context.Background(), "web", 0) // rung 0 is the VPN pool
	if direct.called {
		t.Error("an inactive VPN rung must use the pool delay test, not the direct prober")
	}
	if !got.OK || got.RTTms != 42 {
		t.Errorf("verdict = %+v, want the delay-test RTT 42", got)
	}
}

// In tun mode there is no direct prober, because a direct dial from this process
// is captured by our own tun. The old fallback — delegate to base — meant the
// packet followed the service's route rule to whatever the selector pointed at,
// so an inactive DESYNC rung was credited with the active VPN node's health.
// Measured on the ThinkPad: silent probes of positions 0 and 1 accumulated
// successes toward a recovery onto a rung that did not work, while YouTube only
// loaded through the VPN. Refusing is the only honest answer available here.
func TestInactiveDirectRungIsUnmeasuredWithoutADirectProber(t *testing.T) {
	base := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 26}}
	// Selector sits on a VPN node, so rung 1 (direct) is INACTIVE.
	p := NewRungProber(base, clashStub(t, "vpn_pool", 42, false), rungReg(), "", 0)

	got := p.Probe(context.Background(), "web", 1)
	if base.called {
		t.Error("the ordinary path probe measures the VPN node, not the direct rung — it must not be consulted")
	}
	if !got.Unmeasured {
		t.Fatal("an unreachable rung must be reported as unmeasured, not judged")
	}
	if got.OK {
		t.Error("unmeasured must not read as a success: that is the false recovery this prevents")
	}
	if got.Err == "" {
		t.Error("a component that declines must say why")
	}

	// With a direct prober wired (proxy mode) the rung IS measurable, and the
	// refusal must not survive into a configuration that can answer.
	direct := &fakeBase{verdict: events.ProductionVerdict{OK: true, RTTms: 11}}
	p.SetDirectProber(direct)
	if got := p.Probe(context.Background(), "web", 1); got.Unmeasured || !got.OK {
		t.Errorf("with a direct prober the rung is measurable, got %+v", got)
	}
	if !direct.called {
		t.Error("the direct prober must be the one consulted")
	}
}
