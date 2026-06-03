// Package pools groups nodes into named pools by capability and tag filters
// (spec 4.3). It is pure logic: given a node set and pool definitions it
// computes membership. Turning a pool into a live sing-box url-test group is a
// later, hardware-adjacent step; here we only decide who belongs where.
package pools

import "github.com/strace-me/lotsman/pkg/subscription"

// Capability names usable in a Filter.
const (
	CapTCP       = "tcp"
	CapUDPNative = "udp_native"
)

// Pool types. Only url_test is meaningful in this phase; speedtest_top and
// friends arrive with the bulk pool later.
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
	Name        string
	Type        string
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

// Set is a collection of pools keyed by name.
type Set struct {
	Pools map[string]Pool
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
	for name, p := range s.Pools {
		out[name] = p.Members(nodes)
	}
	return out
}
