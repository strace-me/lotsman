// Package vpnbalance actively health-checks the concrete nodes behind a
// sing-box VPN selector and repoints the selector at the best-scoring live
// node. It closes the gap that bit us in production: sing-box url-test re-probes
// on a lazy interval and "sticks" to its last-good pick, so a node that dies
// mid-interval can strand all VPN traffic on a dead tunnel. Lotsman probes each
// node on demand (Clash API /delay), ranks them with pkg/balancer, and switches.
package vpnbalance

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/quality"
)

// API is the slice of the Clash client vpnbalance needs (real impl:
// *dataplane.ClashClient). Kept narrow so tests can fake it.
type API interface {
	Proxy(ctx context.Context, name string) (dataplane.ProxyInfo, error)
	NodeDelay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error)
	SetSelector(ctx context.Context, selector, target string) error
}

// Balancer rebalances one selector group.
type Balancer struct {
	api      API
	selector string
	testURL  string
	samples  int
	timeout  time.Duration
	weights  balancer.Weights
	dryRun   bool
	log      *slog.Logger
}

// New builds a Balancer. samples is how many delay probes per node (>1 yields a
// jitter signal); weights come from balancer.ProfileFor(category). When dryRun
// is set the node probes still run (read-only) but the selector is not switched.
func New(api API, selector, testURL string, samples int, w balancer.Weights, dryRun bool, log *slog.Logger) *Balancer {
	if samples < 1 {
		samples = 1
	}
	return &Balancer{
		api: api, selector: selector, testURL: testURL,
		samples: samples, timeout: 5 * time.Second, weights: w, dryRun: dryRun, log: log,
	}
}

// Rebalance probes each concrete node behind the selector, ranks them, and
// repoints the selector at the best live node when it differs from the current
// pick. Nested groups (url-test/selector members) are skipped — we pin concrete
// nodes so we bypass the stuck-url-test behavior entirely.
func (b *Balancer) Rebalance(ctx context.Context) error {
	info, err := b.api.Proxy(ctx, b.selector)
	if err != nil {
		return err
	}
	cands := make([]balancer.Candidate, 0, len(info.All))
	for _, m := range info.All {
		mi, err := b.api.Proxy(ctx, m)
		if err != nil {
			b.log.Warn("vpnbalance: member query failed", "member", m, "err", err.Error())
			continue
		}
		if isGroup(mi.Type) {
			continue // skip nested url-test/selector; pin concrete nodes only
		}
		cands = append(cands, balancer.Candidate{ID: m, Q: b.probe(ctx, m)})
	}
	if len(cands) == 0 {
		return fmt.Errorf("vpnbalance: no concrete nodes behind %q", b.selector)
	}

	ranked := balancer.Rank(cands, b.weights)
	best := ranked[0]
	if best.Q.Loss >= 1 { // every node failed every probe
		b.log.Warn("vpnbalance: all nodes down", "selector", b.selector, "nodes", len(cands))
		return nil
	}
	if info.Now != best.ID {
		if b.dryRun {
			b.log.Info("vpnbalance: dry-run would switch node", "selector", b.selector,
				"from", info.Now, "to", best.ID, "p95ms", best.Q.P95ms, "loss", best.Q.Loss)
			return nil
		}
		if err := b.api.SetSelector(ctx, b.selector, best.ID); err != nil {
			return err
		}
		b.log.Info("vpnbalance: switched node", "selector", b.selector,
			"from", info.Now, "to", best.ID, "p95ms", best.Q.P95ms, "loss", best.Q.Loss)
		return nil
	}
	b.log.Info("vpnbalance: best node already active", "selector", b.selector,
		"now", info.Now, "p95ms", best.Q.P95ms, "loss", best.Q.Loss)
	return nil
}

// probe runs samples delay tests; failures count as loss in the Quality.
func (b *Balancer) probe(ctx context.Context, node string) quality.Quality {
	rtts := make([]float64, 0, b.samples)
	for i := 0; i < b.samples; i++ {
		if d, err := b.api.NodeDelay(ctx, node, b.testURL, b.timeout); err == nil {
			rtts = append(rtts, float64(d))
		}
	}
	return quality.FromRTTs(rtts, b.samples)
}

func isGroup(t string) bool {
	switch strings.ToLower(t) {
	case "urltest", "selector", "fallback", "loadbalance":
		return true
	}
	return false
}
