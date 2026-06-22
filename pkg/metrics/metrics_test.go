package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/misroute"
	"github.com/strace-me/lotsman/pkg/noderank"
	"github.com/strace-me/lotsman/pkg/observe"
	"github.com/strace-me/lotsman/pkg/remediate"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// scrape drives the collector's /metrics handler and returns the body.
func scrape(t *testing.T, c *Collector) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	c.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type=%q, want text/plain exposition format", ct)
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, body, line string) {
	t.Helper()
	if !strings.Contains(body, line) {
		t.Errorf("metrics output missing line:\n  %s\n--- full output ---\n%s", line, body)
	}
}

func mustNotContain(t *testing.T, body, frag string) {
	t.Helper()
	if strings.Contains(body, frag) {
		t.Errorf("metrics output unexpectedly contains %q:\n%s", frag, body)
	}
}

// TestServeHTTPRendersAllSections wires every snapshot source and asserts each
// metric family renders with the right labels and values, including the LOT-35
// oneway_udp_ratio gauge and the b2i bool encoding for broken/misrouted/proposed.
func TestServeHTTPRendersAllSections(t *testing.T) {
	c := New(
		func() []brain.ServiceState {
			// returned unsorted on purpose — the handler must sort by service.
			return []brain.ServiceState{
				{Service: "youtube", Position: 2, State: "vpn", Fails: 3, Broken: false},
				{Service: "discord", Position: 0, State: "zapret", Fails: 0, Broken: true},
			}
		},
		func() map[string]float64 {
			return map[string]float64{"youtube|vpn_url_test": 0.9, "discord|alt12": 0.5}
		},
	)
	c.SetObserveSnapshot(func() observe.Snapshot {
		return observe.Snapshot{Services: map[string]observe.ServiceMetrics{
			"discord": {Service: "discord", Flows: 6, Bytes: 4242, LeakRatio: 0, DeadFlowRatio: 0.8, OneWayUDPRatio: 0.75},
		}}
	})
	c.SetMisrouteSnapshot(func() []misroute.Verdict {
		return []misroute.Verdict{{Service: "youtube", Misrouted: true, Kind: "dead"}}
	})
	c.SetRemediationSnapshot(func() []remediate.Plan {
		return []remediate.Plan{{Service: "youtube", Action: remediate.ActionRejectQUIC}}
	})
	c.SetSubscriptionSnapshot(func() map[string]subscription.Userinfo {
		return map[string]subscription.Userinfo{
			"vpn-a": {Upload: 30, Download: 70, Total: 1000}, // 10% used, no expiry
		}
	})
	c.ObserveProbe("youtube", true, 12)
	c.ObserveProbe("youtube", true, 14)
	c.ObserveProbe("youtube", false, 0)

	body := scrape(t, c)

	// Brain state (sorted: discord before youtube).
	mustContain(t, body, `lotsman_service_position{service="discord",state="zapret"} 0`)
	mustContain(t, body, `lotsman_service_position{service="youtube",state="vpn"} 2`)
	mustContain(t, body, `lotsman_service_broken{service="discord"} 1`)
	mustContain(t, body, `lotsman_service_broken{service="youtube"} 0`)
	mustContain(t, body, `lotsman_active_fails{service="youtube"} 3`)

	// Probe counters.
	mustContain(t, body, `lotsman_probe_total{service="youtube",result="ok"} 2`)
	mustContain(t, body, `lotsman_probe_total{service="youtube",result="fail"} 1`)

	// KB EWMA: "svc|strat" key split into labels, 4-decimal value.
	mustContain(t, body, `lotsman_strategy_ewma{service="youtube",strategy="vpn_url_test"} 0.9000`)
	mustContain(t, body, `lotsman_strategy_ewma{service="discord",strategy="alt12"} 0.5000`)

	// Observe section, incl. the LOT-35 one-way RTC gauge.
	mustContain(t, body, `lotsman_service_dead_flow_ratio{service="discord"} 0.8000`)
	mustContain(t, body, `lotsman_service_oneway_udp_ratio{service="discord"} 0.7500`)
	mustContain(t, body, `lotsman_service_flows{service="discord"} 6`)
	mustContain(t, body, `lotsman_service_bytes{service="discord"} 4242`)

	// Misroute + remediation gauges.
	mustContain(t, body, `lotsman_service_misrouted{service="youtube",kind="dead"} 1`)
	mustContain(t, body, `lotsman_service_remediation_proposed{service="youtube",action="reject-quic"} 1`)

	// Subscription quota/expiry (LOT-7): 100/1000 used = 0.1; no expiry -> -1 sentinel.
	mustContain(t, body, `lotsman_subscription_fraction_used{subscription="vpn-a"} 0.1000`)
	mustContain(t, body, `lotsman_subscription_days_until_expire{subscription="vpn-a"} -1.00`)
	mustContain(t, body, `lotsman_subscription_used_bytes{subscription="vpn-a"} 100`)
}

// TestServeHTTPOmitsOptionalSectionsWhenUnset: with only the required Brain+KB
// snapshots wired, the optional observe/misroute/remediation families must be
// entirely absent (nil snapshot fn = feature disabled), not emitted empty.
func TestServeHTTPOmitsOptionalSectionsWhenUnset(t *testing.T) {
	c := New(
		func() []brain.ServiceState { return []brain.ServiceState{{Service: "youtube", State: "vpn"}} },
		func() map[string]float64 { return nil },
	)
	body := scrape(t, c)

	mustContain(t, body, `lotsman_service_position{service="youtube",state="vpn"} 0`)
	mustNotContain(t, body, "lotsman_service_oneway_udp_ratio")
	mustNotContain(t, body, "lotsman_service_leak_ratio")
	mustNotContain(t, body, "lotsman_service_misrouted")
	mustNotContain(t, body, "lotsman_service_remediation_proposed")
	mustNotContain(t, body, "lotsman_subscription_")
}

// TestRemediationProposedEncodesActionNoneAsZero: a Plan whose Action is
// ActionNone is published as 0 (not proposed) while keeping its action label.
func TestRemediationProposedEncodesActionNoneAsZero(t *testing.T) {
	c := New(
		func() []brain.ServiceState { return nil },
		func() map[string]float64 { return nil },
	)
	c.SetRemediationSnapshot(func() []remediate.Plan {
		return []remediate.Plan{
			{Service: "a", Action: remediate.ActionNone},
			{Service: "b", Action: remediate.ActionIPFallback},
		}
	})
	body := scrape(t, c)
	mustContain(t, body, `lotsman_service_remediation_proposed{service="a",action="none"} 0`)
	mustContain(t, body, `lotsman_service_remediation_proposed{service="b",action="ip-fallback"} 1`)
}

// LOT-6: node health renders as state-labeled gauge + consec-fail counter.
func TestServeHTTPRendersNodeHealth(t *testing.T) {
	c := New(func() []brain.ServiceState { return nil }, func() map[string]float64 { return nil })
	c.SetNodeHealthSnapshot(func() []noderank.NodeHealth {
		return []noderank.NodeHealth{
			{Node: "de-1", State: "healthy", ConsecFail: 0},
			{Node: "nl-2", State: "down", ConsecFail: 4},
		}
	})
	body := scrape(t, c)
	mustContain(t, body, `lotsman_node_health{node="de-1",state="healthy"} 1`)
	mustContain(t, body, `lotsman_node_health{node="nl-2",state="down"} 1`)
	mustContain(t, body, `lotsman_node_consec_fail{node="nl-2"} 4`)
}
