// Package metrics exposes Lotsman's state in Prometheus text format on
// /metrics, scrapeable by NetData (already on the R5S) or Prometheus. It
// pulls live state from Brain (positions) and the KB (EWMA), and counts probe
// outcomes it observes from the probing engine. No external metrics library —
// the exposition format is plain text.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/misroute"
	"github.com/strace-me/lotsman/pkg/noderank"
	"github.com/strace-me/lotsman/pkg/observe"
	"github.com/strace-me/lotsman/pkg/remediate"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// Collector gathers metrics and serves them.
type Collector struct {
	brainSnap        func() []brain.ServiceState
	kbSnap           func() map[string]float64
	observeSnap      func() observe.Snapshot                 // optional; nil = observe eye disabled
	misrouteSnap     func() []misroute.Verdict               // optional; nil = misroute detector disabled
	remediationSnap  func() []remediate.Plan                 // optional; nil = remediation planner disabled (LOT-18a)
	subscriptionSnap func() map[string]subscription.Userinfo // optional; nil = no subscription userinfo (LOT-7)
	nodeHealthSnap   func() []noderank.NodeHealth            // optional; nil = noderank disabled (LOT-6)

	mu        sync.Mutex
	probeOK   map[string]int
	probeFail map[string]int
}

// New builds a Collector over the live Brain and KB snapshot functions.
func New(brainSnap func() []brain.ServiceState, kbSnap func() map[string]float64) *Collector {
	return &Collector{
		brainSnap: brainSnap, kbSnap: kbSnap,
		probeOK: map[string]int{}, probeFail: map[string]int{},
	}
}

// SetObserveSnapshot wires the passive-observation eye's latest snapshot
// (LOT-15). The function should return the most recent Snapshot; metrics reads
// it at scrape time. No-op effect until set.
func (c *Collector) SetObserveSnapshot(fn func() observe.Snapshot) { c.observeSnap = fn }

// SetMisrouteSnapshot wires the misroute detector's latest verdicts (LOT-16).
// The function should return the most recent Detect result; metrics reads it at
// scrape time and publishes lotsman_service_misrouted. No-op effect until set.
func (c *Collector) SetMisrouteSnapshot(fn func() []misroute.Verdict) { c.misrouteSnap = fn }

// SetRemediationSnapshot wires the remediation planner's latest proposed plans
// (LOT-18a, PROPOSE-ONLY). The function should return the most recent set of
// non-none plans; metrics reads it at scrape time and publishes
// lotsman_service_remediation_proposed. No-op effect until set.
func (c *Collector) SetRemediationSnapshot(fn func() []remediate.Plan) { c.remediationSnap = fn }

// SetSubscriptionSnapshot wires per-subscription quota/expiry (LOT-7). The
// function returns the latest userinfo keyed by declaration name; metrics reads
// it at scrape time and publishes lotsman_subscription_*. No-op until set.
func (c *Collector) SetSubscriptionSnapshot(fn func() map[string]subscription.Userinfo) {
	c.subscriptionSnap = fn
}

// SetNodeHealthSnapshot wires noderank's per-node health (LOT-6): surfaces the
// health the ranker already tracks (no duplicate Tracker). Publishes
// lotsman_node_health / lotsman_node_consec_fail. No-op until set.
func (c *Collector) SetNodeHealthSnapshot(fn func() []noderank.NodeHealth) { c.nodeHealthSnap = fn }

// ObserveProbe records a probe outcome. Implements probing.ProbeObserver.
func (c *Collector) ObserveProbe(service string, ok bool, _ int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		c.probeOK[service]++
	} else {
		c.probeFail[service]++
	}
}

// Serve starts the metrics HTTP server in a goroutine. Returns the server so
// the caller can shut it down.
func (c *Collector) Serve(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", c)
	srv := &http.Server{Addr: addr, Handler: mux}
	go srv.ListenAndServe()
	return srv
}

func (c *Collector) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	var b strings.Builder

	b.WriteString("# HELP lotsman_service_position Current chain position per service.\n")
	b.WriteString("# TYPE lotsman_service_position gauge\n")
	states := c.brainSnap()
	sort.Slice(states, func(i, j int) bool { return states[i].Service < states[j].Service })
	for _, s := range states {
		fmt.Fprintf(&b, "lotsman_service_position{service=%q,state=%q} %d\n", s.Service, s.State, s.Position)
	}

	b.WriteString("# HELP lotsman_service_broken Whether a service has exhausted its chain (1) or not (0).\n")
	b.WriteString("# TYPE lotsman_service_broken gauge\n")
	for _, s := range states {
		fmt.Fprintf(&b, "lotsman_service_broken{service=%q} %d\n", s.Service, b2i(s.Broken))
	}

	b.WriteString("# HELP lotsman_active_fails Consecutive active-probe failures for the current strategy.\n")
	b.WriteString("# TYPE lotsman_active_fails gauge\n")
	for _, s := range states {
		fmt.Fprintf(&b, "lotsman_active_fails{service=%q} %d\n", s.Service, s.Fails)
	}

	b.WriteString("# HELP lotsman_probe_total Total probes observed, by outcome.\n")
	b.WriteString("# TYPE lotsman_probe_total counter\n")
	c.mu.Lock()
	for _, svc := range sortedKeys(c.probeOK, c.probeFail) {
		fmt.Fprintf(&b, "lotsman_probe_total{service=%q,result=\"ok\"} %d\n", svc, c.probeOK[svc])
		fmt.Fprintf(&b, "lotsman_probe_total{service=%q,result=\"fail\"} %d\n", svc, c.probeFail[svc])
	}
	c.mu.Unlock()

	b.WriteString("# HELP lotsman_strategy_ewma Learned EWMA success rate per (service,strategy).\n")
	b.WriteString("# TYPE lotsman_strategy_ewma gauge\n")
	snap := c.kbSnap()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		svc, strat, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "lotsman_strategy_ewma{service=%q,strategy=%q} %s\n",
			svc, strat, strconv.FormatFloat(snap[k], 'f', 4, 64))
	}

	if c.observeSnap != nil {
		osnap := c.observeSnap()
		svcs := make([]string, 0, len(osnap.Services))
		for s := range osnap.Services {
			svcs = append(svcs, s)
		}
		sort.Strings(svcs)

		b.WriteString("# HELP lotsman_service_leak_ratio Fraction of a service's live flows that went direct while it should have been tunnelled.\n")
		b.WriteString("# TYPE lotsman_service_leak_ratio gauge\n")
		for _, s := range svcs {
			fmt.Fprintf(&b, "lotsman_service_leak_ratio{service=%q} %s\n", s, strconv.FormatFloat(osnap.Services[s].LeakRatio, 'f', 4, 64))
		}

		b.WriteString("# HELP lotsman_service_dead_flow_ratio Fraction of a service's matched UDP/QUIC flows that are stalled (~0 download).\n")
		b.WriteString("# TYPE lotsman_service_dead_flow_ratio gauge\n")
		for _, s := range svcs {
			fmt.Fprintf(&b, "lotsman_service_dead_flow_ratio{service=%q} %s\n", s, strconv.FormatFloat(osnap.Services[s].DeadFlowRatio, 'f', 4, 64))
		}

		b.WriteString("# HELP lotsman_service_oneway_udp_ratio Fraction of a service's matched UDP flows that are sending with no return (wedged one-way RTC signature, LOT-35).\n")
		b.WriteString("# TYPE lotsman_service_oneway_udp_ratio gauge\n")
		for _, s := range svcs {
			fmt.Fprintf(&b, "lotsman_service_oneway_udp_ratio{service=%q} %s\n", s, strconv.FormatFloat(osnap.Services[s].OneWayUDPRatio, 'f', 4, 64))
		}

		b.WriteString("# HELP lotsman_service_flows Live connections matched to a service in the last observe pass.\n")
		b.WriteString("# TYPE lotsman_service_flows gauge\n")
		for _, s := range svcs {
			fmt.Fprintf(&b, "lotsman_service_flows{service=%q} %d\n", s, osnap.Services[s].Flows)
		}

		b.WriteString("# HELP lotsman_service_bytes Total upload+download bytes across a service's live flows.\n")
		b.WriteString("# TYPE lotsman_service_bytes gauge\n")
		for _, s := range svcs {
			fmt.Fprintf(&b, "lotsman_service_bytes{service=%q} %d\n", s, osnap.Services[s].Bytes)
		}
	}

	if c.misrouteSnap != nil {
		vs := c.misrouteSnap()
		sort.Slice(vs, func(i, j int) bool { return vs[i].Service < vs[j].Service })
		b.WriteString("# HELP lotsman_service_misrouted Whether the misroute detector flagged a service (1) or not (0).\n")
		b.WriteString("# TYPE lotsman_service_misrouted gauge\n")
		for _, v := range vs {
			fmt.Fprintf(&b, "lotsman_service_misrouted{service=%q,kind=%q} %d\n", v.Service, v.Kind, b2i(v.Misrouted))
		}
	}

	if c.remediationSnap != nil {
		ps := c.remediationSnap()
		sort.Slice(ps, func(i, j int) bool { return ps[i].Service < ps[j].Service })
		b.WriteString("# HELP lotsman_service_remediation_proposed Whether a remediation was proposed for a service (1) or not (0). PROPOSE-ONLY (LOT-18a): not applied.\n")
		b.WriteString("# TYPE lotsman_service_remediation_proposed gauge\n")
		for _, p := range ps {
			fmt.Fprintf(&b, "lotsman_service_remediation_proposed{service=%q,action=%q} %d\n", p.Service, p.Action, b2i(p.Action != remediate.ActionNone))
		}
	}

	if c.subscriptionSnap != nil {
		ui := c.subscriptionSnap()
		subs := make([]string, 0, len(ui))
		for s := range ui {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		now := time.Now()

		b.WriteString("# HELP lotsman_subscription_fraction_used Fraction of a subscription's quota consumed (-1 = total unknown/unlimited).\n")
		b.WriteString("# TYPE lotsman_subscription_fraction_used gauge\n")
		for _, s := range subs {
			fmt.Fprintf(&b, "lotsman_subscription_fraction_used{subscription=%q} %s\n", s, strconv.FormatFloat(ui[s].FractionUsed(), 'f', 4, 64))
		}

		b.WriteString("# HELP lotsman_subscription_days_until_expire Days until a subscription expires (-1 = no expiry, negative = expired).\n")
		b.WriteString("# TYPE lotsman_subscription_days_until_expire gauge\n")
		for _, s := range subs {
			fmt.Fprintf(&b, "lotsman_subscription_days_until_expire{subscription=%q} %s\n", s, strconv.FormatFloat(ui[s].DaysUntilExpire(now), 'f', 2, 64))
		}

		b.WriteString("# HELP lotsman_subscription_used_bytes Bytes consumed (upload+download) by a subscription.\n")
		b.WriteString("# TYPE lotsman_subscription_used_bytes gauge\n")
		for _, s := range subs {
			fmt.Fprintf(&b, "lotsman_subscription_used_bytes{subscription=%q} %d\n", s, ui[s].Used())
		}
	}

	if c.nodeHealthSnap != nil {
		hs := c.nodeHealthSnap() // already sorted by node
		b.WriteString("# HELP lotsman_node_health Per-node health from the ranker: 1 on the node's current state row (healthy/degraded/down).\n")
		b.WriteString("# TYPE lotsman_node_health gauge\n")
		for _, h := range hs {
			fmt.Fprintf(&b, "lotsman_node_health{node=%q,state=%q} 1\n", h.Node, h.State)
		}
		b.WriteString("# HELP lotsman_node_consec_fail Consecutive failed probes for a node (ranker hysteresis).\n")
		b.WriteString("# TYPE lotsman_node_consec_fail gauge\n")
		for _, h := range hs {
			fmt.Fprintf(&b, "lotsman_node_consec_fail{node=%q} %d\n", h.Node, h.ConsecFail)
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte(b.String()))
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sortedKeys(a, b map[string]int) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
