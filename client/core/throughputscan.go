package core

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/throughput"
)

// throughputURLs are what the scan pulls. They must have BODIES: a 204 returns
// nothing, and a canary that scored one at 0 KiB/s once demoted every strategy it
// ever tried, so an endpoint that is too small measures the file rather than the
// path.
var throughputURLs = []string{
	"https://speed.cloudflare.com/__down?bytes=2000000",
	"https://proof.ovh.net/files/10Mb.dat",
}

// clientSelector aims the shared probe selector by NAME, remembering what it
// found and putting it back on every exit.
//
// `sel-probe` is select-by-name, so a scan that leaves it aimed at the node it
// last measured makes the NEXT CANARY of any rule report on a node the operator
// never chose — and that report is written into the KB as if it described the
// rule. Measured live 2026-08-19 for the canary's inbound, the same way.
type clientSelector struct {
	api    *dataplane.ClashClient
	tags   map[string]string
	tagFor func(id string) (string, bool)
}

func (c *clientSelector) PointAt(ctx context.Context, node string) (string, error) {
	info, err := c.api.Proxy(ctx, singbox.ProbeSelector)
	if err != nil {
		return "", err
	}
	// sing-box selects by OUTBOUND TAG; the scan reasons in node ids.
	tag := node
	if t, ok := c.tags[node]; ok {
		tag = t
	}
	if err := c.api.SetSelector(ctx, singbox.ProbeSelector, tag); err != nil {
		return "", err
	}
	return info.Now, nil
}

func (c *clientSelector) Current(ctx context.Context) (string, error) {
	info, err := c.api.Proxy(ctx, singbox.ProbeSelector)
	return info.Now, err
}

func tagMapFor(nodes []subscription.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if t, ok := singbox.NodeTag(n); ok {
			out[n.ID] = t
		}
	}
	return out
}

// buildThroughputScan builds the client's scan, or explains why it cannot run. It
// returns nil when the feature is off, which is the default.
func (c *Core) buildThroughputScan() (func(context.Context), error) {
	if !c.opts.ThroughputScan {
		return nil, nil
	}
	if c.opts.ProbeProxy == "" {
		return nil, fmt.Errorf("-throughput-scan needs -probe-proxy: the pull must travel the probe inbound, not the box's own egress")
	}
	budget := c.opts.ThroughputBudget
	if budget <= 0 {
		budget = 4
	}
	interval := c.opts.ThroughputInterval
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	sel := &clientSelector{api: c.clash}
	store := throughput.OpenStore("")
	probe := &throughput.SelectorProber{
		Selector: sel,
		ClientFor: func(context.Context) (*http.Client, error) {
			return dataplane.SocksClient(c.opts.ProbeProxy, 30*time.Second), nil
		},
		URLs:   throughputURLs,
		Settle: 2 * time.Second,
		OnRestore: func(err error) {
			// Loud: a failed restore leaves the probe path on the node just scanned,
			// so the next canary of any rule would describe it.
			c.log.Error("throughput scan could not restore the probe path", "err", err)
		},
	}
	sel.tagFor = func(id string) (string, bool) { t, ok := sel.tags[id]; return t, ok }
	ct := &clientThroughput{core: c, interval: interval, store: store, probe: probe, sel: sel, budget: budget, log: c.log}
	c.log.Info("throughput scan enabled", "budget_per_pass", budget, "interval", interval.String(),
		"path", singbox.ProbeSelector,
		"cost", "pulls real bytes per node; a speedtest_top pool stays wide until coverage is real")
	return ct.loop, nil
}

type clientThroughput struct {
	core     *Core
	interval time.Duration
	store    *throughput.Store
	probe    *throughput.SelectorProber
	sel      *clientSelector
	budget   int
	log      *slog.Logger
}

// loop is the runner the caller starts. It takes the same context the other
// background loops get, so a shutdown cancels the scan like everything else.
func (c *clientThroughput) loop(ctx context.Context) {
	interval := c.interval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// The client's own fleet loader, so the scan measures the fleet actually in
		// force — and does not add a second fetch path next to it.
		nodes, _ := c.core.loadNodes(ctx)
		if len(nodes) == 0 {
			// Empty is NORMAL when every subscription failed, and it is not the
			// scanner's to report: the refresh loop already said so.
			continue
		}
		// Aim by tag, resolved once from the fleet just read.
		c.sel.tags = tagMapFor(nodes)
		sc := &throughput.Scanner{Store: c.store, Measure: c.probe.Measure, Log: clientScanLog{c.log}}
		report := sc.Pass(ctx, throughput.SortNodes(nodeIDsOf(nodes)), c.budget)
		covered, coverage := c.store.Coverage(throughput.SortNodes(nodeIDsOf(nodes)))
		c.log.Info("throughput pass", "attempted", report.Attempted, "measured", report.Measured,
			"failed", report.Failed, "fleet", len(nodes), "covered", covered, "coverage", coverage)
	}
}

type clientScanLog struct{ log *slog.Logger }

func (s clientScanLog) Info(msg string, args ...any) { s.log.Info(msg, args...) }
func (s clientScanLog) Warn(msg string, args ...any) { s.log.Warn(msg, args...) }

func nodeIDsOf(nodes []subscription.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}
