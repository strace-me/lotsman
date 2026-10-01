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
// nodes supplies the node set to rank. It is a FUNCTION, not a list, and that is
// the whole point of the signature: the caller decides which set is authoritative
// and both the config generator and this ranker go through the same one.
//
// This used to fetch subscriptions itself, privately, every cycle — while the
// generator was using its own last-good snapshot. So when a subscription fetch
// degraded, the box kept routing on 94 nodes it already had, and the ranker saw
// one. It did not know: it ranked what it was handed and reported "all eligible
// nodes down", 58 times in one overnight run, every one of them with eligible=1
// and the same single node. A fleet that was 94-strong and busy was being called
// dead because two sources of truth had drifted apart — the same failure as the
// list-tag and preflight-release ones, one level up and worse, because membership
// decides which nodes exist as candidates at all.
//
// Resilience is the caller's job and belongs in one place. Here, the only rule is
// that the number is visible: a ranker about to rank three nodes says so, instead
// of letting a shrunken set read as a verdict on the nodes.
func Run(ctx context.Context, conf *config.Config, reg *registry.Registry, ranker *noderank.Ranker, nodes func(context.Context) []subscription.Node, nudge func(string), log *slog.Logger) error {
	if len(conf.Subscriptions) == 0 {
		return nil
	}
	all := nodes(ctx)
	// Visible, not Debug. I first wrote this at Debug with a comment saying the
	// size goes to the log, and the client hardcodes LevelInfo with no flag to
	// raise it — so the line was invisible in production and the claim in the
	// commit message was false. The test passed anyway, because I built its logger
	// at LevelDebug. A visibility guarantee has to be asserted at the level that
	// actually runs, or it is not a guarantee.
	//
	// Warn only when it is empty. A small fleet is routine; an EMPTY one means the
	// caller handed us nothing, and every service is about to be declared node-less
	// for a reason that has nothing to do with the nodes.
	if len(all) == 0 {
		log.Warn("noderank: the authoritative node set is EMPTY — every service will look node-less",
			"nodes", 0, "note", "this is the fleet, not a pool; if generation still routes, the ranker and the generator are reading different things again")
	} else {
		log.Debug("noderank: ranking this node set", "nodes", len(all))
	}
	memberships := conf.Pools.Memberships(all)
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
