// Package pools groups nodes into named pools by capability and tag filters
// (spec 4.3). It is pure logic: given a node set and pool definitions it
// computes membership. Turning a pool into a live sing-box url-test group is a
// later, hardware-adjacent step; here we only decide who belongs where.
package pools

import (
	"sort"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// Capability names usable in a Filter.
const (
	CapTCP       = "tcp"
	CapUDPNative = "udp_native"
)

// Pool types.
const (
	TypeURLTest = "url_test"
)

// Filter selects nodes for a pool.
//
//	Caps:             node must satisfy ALL listed capabilities (AND).
//	TagsInclude:      node must carry at least ONE listed tag (OR); empty = no
//	                  include constraint.
//	TagsExclude:      node must carry NONE of the listed tags.
//	CountriesInclude: node's exit country must be one of these (OR); empty = any.
//	CountriesExclude: node's exit country must be NONE of these (e.g. [ru] keeps
//	                  RF-blocked services off RU-exit nodes). A node with unknown
//	                  country ("") is never excluded — "smart location" nodes pass.
type Filter struct {
	Caps             []string
	TagsInclude      []string
	TagsExclude      []string
	CountriesInclude []string
	CountriesExclude []string
}

// Pool is a named, typed filter over the node set. Warmup/Interval/IdleTimeout
// tune the generated sing-box url-test group (the generator reads them via
// singbox.PoolOptionsFrom): a warmup pool is kept always-hot so failover is
// instant rather than waiting on a cold re-probe.
type Pool struct {
	Name string
	Type string
	// MinCoverage is the fraction of admitted candidates that must carry a
	// measurement before a speedtest_top pool narrows at all.
	//
	// It exists because the measurement is INBOUND: `observe` counts bytes per
	// exit, so it is blind to exits nobody is using. That blindness is correct for
	// "which of the exits in play is better" and fatal for "who should be in
	// play". Left unguarded, LOT-69's own state makes it worse instead of better:
	// ten rules on one exit means only that exit is ever measured, so a
	// throughput ranking keeps exactly that exit and the pool collapses to a
	// single member — looking like the pool was narrowed successfully, while
	// locking in the monoculture it was meant to break up.
	//
	// Below the threshold the pool keeps its whole filter and Reports says why.
	MinCoverage float64
	Limit       int // TypeSpeedtestTop: keep this many fastest nodes; 0/≥candidates = no narrowing
	Filter      Filter
	Warmup      bool   // never let this pool sleep; keep its nodes continuously probed for instant failover
	Interval    string // url-test probe period (e.g. "1m"); "" = generator default (5m, or 1m when Warmup)
	IdleTimeout string // stop probing after this idle time; "" = sing-box default; warmup forces "0s" (never idle)
}

// Members returns the nodes matching the pool's filter, preserving input order.
func (p Pool) Members(nodes []subscription.Node) []subscription.Node {
	out := make([]subscription.Node, 0, len(nodes))
	for _, n := range nodes {
		if p.Filter.matches(n) {
			out = append(out, n)
		}
	}
	return out
}

func (f Filter) matches(n subscription.Node) bool {
	for _, c := range f.Caps {
		if !hasCap(n, c) {
			return false
		}
	}
	if len(f.TagsInclude) > 0 && !hasAnyTag(n, f.TagsInclude) {
		return false
	}
	if hasAnyTag(n, f.TagsExclude) {
		return false
	}
	if len(f.CountriesInclude) > 0 && !inList(n.Country, f.CountriesInclude) {
		return false
	}
	// Unknown country ("") is never excluded — a "smart location" node passes.
	if n.Country != "" && inList(n.Country, f.CountriesExclude) {
		return false
	}
	return true
}

func inList(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func hasCap(n subscription.Node, cap string) bool {
	switch cap {
	case CapTCP:
		return n.Caps.TCP
	case CapUDPNative:
		return n.Caps.UDPNative
	default:
		return false // unknown capability matches nothing
	}
}

func hasAnyTag(n subscription.Node, tags []string) bool {
	for _, want := range tags {
		for _, have := range n.Tags {
			if have == want {
				return true
			}
		}
	}
	return false
}

// Set is a collection of pools keyed by name, plus the throughput measurements
// that a speedtest_top pool ranks by.
//
// Goodput hangs off the Set rather than being a parameter to Memberships because
// that function has six call sites across both roots (daemon, client, reconcile,
// one-shot CLI, nodepass), and a new argument there is a change every front-end
// has to remember to make. This project has already paid for a client that wired
// a subset of the daemon for months (LOT-65): each root tested what it had, both
// were green, and the only detector was a service misbehaving on the owner's
// device. A measurement belongs where membership is computed, not in six
// hand-ups.
//
// A pool that needs a ranking and finds no measurements degrades to its own
// filter and says so in Reports. That is the one-shot CLI's honest state: it has
// no measurements because it measured nothing, and it must not pretend
// otherwise.
type Set struct {
	Pools   map[string]Pool
	Goodput map[string]float64 // node ID -> KiB/s; absent = unmeasured
	// Reports carries what each pool could not decide, keyed by pool name, filled
	// by Memberships. It exists so the "pool narrowed to nothing" and "nothing was
	// measured" cases can be SAID by the caller instead of being inferred later
	// from a config that looks fine.
	Reports map[string]ThroughputReport
}

// Builtin returns the default pools from the spec (4.3.1):
//   - vpn_url_test      tcp nodes, excluding emergency
//   - vpn_url_test_udp  udp-native nodes, excluding emergency
//   - emergency_pool    nodes tagged emergency (last resort)
func Builtin() *Set {
	return &Set{Pools: map[string]Pool{
		"vpn_url_test": {
			Name: "vpn_url_test", Type: TypeURLTest,
			Filter: Filter{Caps: []string{CapTCP}, TagsExclude: []string{"emergency"}},
		},
		"vpn_url_test_udp": {
			Name: "vpn_url_test_udp", Type: TypeURLTest,
			Filter: Filter{Caps: []string{CapUDPNative}, TagsExclude: []string{"emergency"}},
		},
		"emergency_pool": {
			Name: "emergency_pool", Type: TypeURLTest,
			Filter: Filter{TagsInclude: []string{"emergency"}},
		},
	}}
}

// Memberships computes the node list for every pool in the set.
func (s *Set) Memberships(nodes []subscription.Node) map[string][]subscription.Node {
	out := make(map[string][]subscription.Node, len(s.Pools))
	reports := make(map[string]ThroughputReport, len(s.Pools))
	for name, p := range s.Pools {
		// Only a speedtest_top pool consults measurements, and only when it has
		// some. Every other type is membership by filter, exactly as before.
		if p.Type == TypeSpeedtestTop && len(s.Goodput) > 0 {
			members, report := p.TopByThroughput(nodes, s.Goodput, p.Limit)
			out[name] = members
			reports[name] = report
			continue
		}
		members := p.Members(nodes)
		out[name] = members
		if p.Type == TypeSpeedtestTop {
			// A throughput pool with no measurements at all: the filter's members
			// are kept, and the report records that the ranking never ran.
			reports[name] = ThroughputReport{Unmeasured: len(members), Ranked: 0, Kept: len(members), Filled: true}
		}
	}
	if s.Reports == nil {
		s.Reports = make(map[string]ThroughputReport, len(reports))
	}
	for name, report := range reports {
		s.Reports[name] = report
	}
	return out
}

// TypeSpeedtestTop is a pool whose MEMBERSHIP is chosen by measured throughput
// rather than by which node happens to be reachable.
//
// It is deliberately NOT a sing-box selector type. sing-box offers exactly two —
// `selector` and `urltest` — and both pick by latency, so "the fastest exit" is
// not expressible as a selector. Expressing it as one is the trap this type
// exists to avoid: a throughput-ranked group that sing-box then re-ranks by
// latency is a group ranked by latency with extra steps.
//
// Instead the ranking happens HERE, on our own measurements, and the result is a
// narrowed member list that sing-box's urltest then chooses within. The
// throughput decides who is in the room; latency decides who answers. Those are
// different questions and LOT-69 is the measured proof that conflating them
// collapses 108 nodes onto one exit: the lowest-latency node wins ten rules at
// once, and it answers in 40ms while carrying nothing.
const TypeSpeedtestTop = "speedtest_top"

// DefaultMinCoverage is the fraction of a pool's candidates that must be measured
// before a throughput ranking is allowed to narrow it. Half: a pool measured
// across a real fleet lands well above this, and a pool observed only through one
// busy exit lands at a small fraction, which is the case the gate exists to catch.
const DefaultMinCoverage = 0.5

// ThroughputReport says what a membership decision could not decide, because a
// pool that quietly shrunk is a pool that quietly stopped offering a fallback.
type ThroughputReport struct {
	// Coverage is the measured fraction of the admitted candidates, and
	// Uncovered says whether it fell under MinCoverage and the ranking was
	// therefore not attempted.
	Coverage float64
	// Uncovered marks a decision that was NOT made, so the caller can say so
	// instead of the config looking identical to a successful narrowing.
	Uncovered bool
	// Unmeasured is how many nodes the filter admitted but no measurement covers.
	// They are NOT ranked: guessing a band from an absent measurement is how a
	// dead node wins a throughput race.
	Unmeasured int
	// Ranked is how many nodes the ranking actually ordered.
	Ranked int
	// Kept is the resulting member count.
	Kept int
	// Filled reports whether the pool would have been empty and unmeasured nodes
	// were admitted to keep it usable. This is the case that MUST be loud: an
	// empty pool makes the rule's rung unreachable, and the symptom is a service
	// that stopped working with nothing in the log about a pool.
	Filled bool
}

// TopByThroughput returns the pool's members narrowed to the Limit nodes with
// the highest measured goodput, keyed by node ID.
//
// The ordering is total and deterministic: goodput descending, then node ID, so
// two runs over the same measurements produce the same pool. A float ordering
// that depended on map iteration would make the generated sing-box config churn,
// and this project has already paid for a restart-per-diff incident (LOT-1).
func (p Pool) TopByThroughput(nodes []subscription.Node, goodput map[string]float64, limit int) ([]subscription.Node, ThroughputReport) {
	var report ThroughputReport
	admitted := p.Members(nodes)
	if limit <= 0 || limit >= len(admitted) {
		// Nothing to narrow to, so nothing is measured and nothing is claimed.
		report.Kept = len(admitted)
		report.Ranked = len(admitted)
		return admitted, report
	}

	type ranked struct {
		node subscription.Node
		kbps float64
	}
	var measured []ranked
	for _, n := range admitted {
		kbps, ok := goodput[n.ID]
		if !ok || kbps <= 0 {
			report.Unmeasured++
			continue
		}
		measured = append(measured, ranked{node: n, kbps: kbps})
	}
	sort.Slice(measured, func(i, j int) bool {
		if measured[i].kbps != measured[j].kbps {
			return measured[i].kbps > measured[j].kbps
		}
		return measured[i].node.ID < measured[j].node.ID
	})
	// Coverage gate. This is a THIRD outcome, distinct from "nothing measured":
	// here some exits are measured and some are not, and ranking on the measured
	// subset alone would silently reward being the only one in use.
	coverage := float64(len(measured)) / float64(len(admitted))
	report.Coverage = coverage
	minCoverage := p.MinCoverage
	if minCoverage <= 0 {
		minCoverage = DefaultMinCoverage
	}
	if len(measured) > 0 && coverage < minCoverage {
		report.Kept = len(admitted)
		report.Uncovered = true
		return admitted, report
	}
	if len(measured) == 0 {
		// Every candidate is unmeasured. Returning empty would make the rung
		// unreachable; returning the filter's nodes at least keeps the previous
		// behaviour. Both facts have to reach the operator, so Filled says this
		// happened rather than the pool looking like a normal one.
		report.Kept = len(admitted)
		report.Filled = true
		return admitted, report
	}
	if len(measured) < limit {
		limit = len(measured)
	}
	out := make([]subscription.Node, 0, limit)
	for _, r := range measured[:limit] {
		out = append(out, r.node)
	}
	// Only now, with the ranking actually done, is Ranked meaningful. Setting it
	// earlier would make a DECLINED ranking indistinguishable from a small one:
	// both would report a non-zero count of ordered nodes.
	report.Ranked = len(measured)
	report.Kept = len(out)
	return out, report
}
