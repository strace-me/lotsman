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
