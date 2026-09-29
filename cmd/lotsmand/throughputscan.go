package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/reconcile"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/throughput"
)

// throughputURLs are what the scan pulls. They must have BODIES: a 204 returns
// nothing, and a canary that scored one at 0 KiB/s once demoted every strategy it
// ever tried, so an endpoint that is too small is a measurement of the file, not
// of the path.
var throughputURLs = []string{
	"https://speed.cloudflare.com/__down?bytes=2000000",
	"https://proof.ovh.net/files/10Mb.dat",
}

// throughputScanState is the daemon-side scan: a budgeted per-pass measurer over
// the fleet, feeding pool membership through the reconciler.
type throughputScanState struct {
	store   *throughput.Store
	probe   *throughput.SelectorProber
	sel     *clashSelector
	budget  int
	nodes   func() []subscription.Node
	metrics *reconcile.Reconciler
	log     *slog.Logger

	mu   sync.Mutex
	last throughput.PassReport
}

func (s *throughputScanState) run(loadNodes func() []subscription.Node) func(context.Context) error {
	return func(ctx context.Context) error {
		nodes := loadNodes()
		if len(nodes) == 0 {
			// An empty fleet is the NORMAL state when every subscription failed, and
			// it is not the scanner's business to report it: the refresh task already
			// says so. A pass over nothing would just be a pass over nothing.
			return nil
		}
		// Aim by OUTBOUND TAG, resolved once from the fleet just read.
		if s.sel.refresh != nil {
			s.sel.refresh()
		}
		sc := &throughput.Scanner{Store: s.store, Measure: s.probe.Measure, Log: scanLog{s.log}}
		report := sc.Pass(ctx, throughput.SortNodes(nodeIDs(nodes)), s.budget)

		s.mu.Lock()
		s.last = report
		s.mu.Unlock()

		measured, coverage := s.store.Coverage(throughput.SortNodes(nodeIDs(nodes)))
		s.log.Info("throughput pass",
			"attempted", report.Attempted, "measured", report.Measured, "failed", report.Failed,
			"fleet", len(nodes), "covered", measured, "coverage", coverage)

		// The gate lives in pkg/pools, and it is deliberately not bypassed here: a
		// thin-coverage pass must leave the pool exactly as wide as it was. What the
		// scan does is RAISE coverage until that is no longer true.
		if report.Attempted == 0 {
			s.log.Info("throughput scan idle: the fleet is covered and nothing is stale yet")
		}
		return nil
	}
}

type scanLog struct{ log *slog.Logger }

func (s scanLog) Info(msg string, args ...any) { s.log.Info(msg, args...) }
func (s scanLog) Warn(msg string, args ...any) { s.log.Warn(msg, args...) }

func nodeIDs(nodes []subscription.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

// clashSelector aims sing-box's shared probe selector by NAME. The previous target
// is remembered and restored on every exit, because leaving it aimed elsewhere is
// the failure that made probes for x and social leave through the youtube canary's
// exit on 2026-08-19.
type clashSelector struct {
	api     *dataplane.ClashClient
	refresh func()
	tags    map[string]string
	tagFor  func(id string) (string, bool)
}

// tagMap builds node-id → outbound-tag once. sing-box selects by tag and the scan
// reasons in node ids, and the conversion is a pure function of the fleet.
func tagMap(nodes []subscription.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if t, ok := singbox.NodeTag(n); ok {
			out[n.ID] = t
		}
	}
	return out
}

func (c *clashSelector) PointAt(ctx context.Context, node string) (string, error) {
	// sing-box selects by OUTBOUND TAG; the scan reasons in node ids, so convert
	// here rather than making the store carry generator vocabulary.
	// sing-box selects by OUTBOUND TAG and the scan reasons in node ids, so the
	// conversion is done here, from the fleet in hand.
	tag := node
	if t, ok := c.tags[node]; ok {
		tag = t
	}
	previous, err := c.api.Proxy(ctx, singbox.ProbeSelector)
	if err != nil {
		return "", err
	}
	if err := c.api.SetSelector(ctx, singbox.ProbeSelector, tag); err != nil {
		return "", err
	}
	return previous.Now, nil
}

func (c *clashSelector) Current(ctx context.Context) (string, error) {
	info, err := c.api.Proxy(ctx, singbox.ProbeSelector)
	return info.Now, err
}

// startThroughputScan builds the scan, or explains why it cannot run.
func startThroughputScan(clash *dataplane.ClashClient, probeProxy string, budget int, rc *reconcile.Reconciler, log *slog.Logger) (*throughputScanState, func() []subscription.Node, error) {
	if probeProxy == "" {
		return nil, nil, fmt.Errorf("-throughput-scan needs -probe-proxy: the pull must travel the probe inbound, not the box's own egress")
	}
	if clash == nil {
		return nil, nil, fmt.Errorf("-throughput-scan needs clash-api to aim the probe selector")
	}
	sel := &clashSelector{api: clash}
	loadNodes := loadSubscriptionNodes(rc)

	// The id→tag map is built ONCE per pass, not per aim. Aiming happens several
	// times inside one pass, and resolving a tag by re-fetching the subscriptions
	// would put a network fetch on the hot path of every single measurement.
	sel.tagFor = func(id string) (string, bool) { return "", false }
	sel.refresh = func() { sel.tags = tagMap(loadNodes()) }
	store := throughput.OpenStore("")
	probe := &throughput.SelectorProber{
		Selector:  sel,
		ClientFor: socksClientFor(probeProxy),
		URLs:      throughputURLs,
		Settle:    2 * time.Second,
		OnRestore: func(err error) {
			// Loud on purpose: a failed restore leaves the probe path aimed at the
			// node just scanned, so the next canary of any rule describes it.
			log.Error("throughput scan could not restore the probe path; the next probe may describe the wrong exit", "err", err)
		},
	}
	return &throughputScanState{store: store, probe: probe, sel: sel, budget: budget, metrics: rc, log: log},
		loadSubscriptionNodes(rc), nil
}

// socksClientFor builds the HTTP client the scan pulls through: the probe inbound,
// which is where the aimed selector takes effect.
func socksClientFor(proxy string) func(context.Context) (*http.Client, error) {
	return func(ctx context.Context) (*http.Client, error) {
		return dataplane.SocksClient(proxy, 30*time.Second), nil
	}
}

// loadSubscriptionNodes re-reads the fleet through the reconciler's own loader.
// A separate read on purpose: the scan must not depend on a reconcile having just
// happened, or it would only ever measure a fleet that was already in force.
func loadSubscriptionNodes(rc *reconcile.Reconciler) func() []subscription.Node {
	return func() []subscription.Node {
		if rc == nil || rc.Loader == nil {
			return nil
		}
		nodes, errs := rc.Loader.Load(context.Background(), rc.Subs)
		for _, err := range errs {
			_ = err // the refresh task already reports fetch problems
		}
		return nodes
	}
}
