// Package nodepass runs one node-ranking pass over every service: load the
// subscriptions, work out which nodes each service's pool contains, and ask the
// ranker to pick the best exit for it.
//
// It lives in its own package because both composition roots need it and neither
// can host it: pkg/singbox already imports pkg/noderank, so the pass cannot go
// inside noderank without a cycle, and a copy in each root is how two statements
// about the same thing drift apart.
//
// It was router-only until 2026-08-11 (LOT-65). The desktop client never called
// it, so executor.VPN's bestNode hook was nil there and every service pointed at
// its pool's url-test group — which ranks by LATENCY, and a node under the TSPU
// volume freeze answers a delay test in 40ms and then carries nothing. The
// laptop was choosing exits by the one measurement that cannot tell a working
// node from a frozen one.
package nodepass

import (
	"context"
	"log/slog"

	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/noderank"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// Run performs one per-service node-ranking cycle: for every service with an HTTP
// probe target and a non-empty VPN pool, it ranks that pool's concrete nodes by
// probing each THROUGH the service's own URL and pins sel-<svc> to the winner.
// Candidates come from the pool membership, which is already country-filtered
// (pools' countries_exclude), so a blocked service never lands on a RU exit even
// if its ping is lowest — the Hiddify/url-test failure mode this whole feature
// exists to fix. Per-cycle subscription reload mirrors loadPools; a single bad
// service is logged, not fatal.
//
// nudge, when non-nil, is called with a service name as soon as its fresh advice
// is ready, so a caller can apply it without waiting for the whole (potentially
// minutes-long) sweep to finish.
func Run(ctx context.Context, conf *config.Config, reg *registry.Registry, ranker *noderank.Ranker, nudge func(string), log *slog.Logger) error {
	if len(conf.Subscriptions) == 0 {
		return nil
	}
	mgr := subscription.NewManager(subscription.NewHTTPFetcher())
	nodes, errs := mgr.Load(ctx, conf.Subscriptions)
	for _, e := range errs {
		log.Warn("noderank: subscription load issue", "err", e)
	}
	memberships := conf.Pools.Memberships(nodes)
	for _, svc := range reg.Services {
		if svc.ProbeType != "" && svc.ProbeType != "http" {
			continue // Clash /delay is an HTTP GET; tcp/stun services are not rankable this way
		}
		if svc.ProbeTarget == "" || (len(svc.RuleSets) == 0 && len(svc.Domains) == 0 && len(svc.IPs) == 0) {
			continue
		}
		pool := svc.VPNPool()
		if pool == "" {
			continue
		}
		members := memberships[pool]
		cands := make([]noderank.Candidate, 0, len(members))
		for _, m := range members {
			if tag, ok := singbox.NodeTag(m); ok {
				cands = append(cands, noderank.Candidate{Tag: tag, Country: m.Country})
			}
		}
		if len(cands) == 0 {
			continue
		}
		prof := svc.Profile
		if prof == "" {
			prof = svc.Category
		}
		ns := noderank.Service{
			Name:     svc.Name,
			Selector: registry.SelectorTag(svc.Name),
			ProbeURL: svc.ProbeTarget,
			Weights:  balancer.ProfileFor(prof),
			Sticky:   svc.Sticky, // LOT-12: honor the declared sticky flag (was a no-op)
		}
		if _, err := ranker.Pick(ctx, ns, cands); err != nil {
			log.Warn("noderank: pick failed", "service", svc.Name, "err", err.Error())
			continue
		}
		// Apply this service's fresh advice now, rather than waiting for the whole
		// (potentially minutes-long) pass over every pool to finish.
		if nudge != nil {
			nudge(svc.Name)
		}
	}
	return nil
}
