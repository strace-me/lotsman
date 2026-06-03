package registry

import (
	"fmt"
	"net"
	"sort"
)

// Conflict is an overlap between two or more services' match inputs: the same
// domain or rule-set tag claimed by several services, or overlapping ip_cidr
// ranges. These are not fatal — route ordering (Priority + the domain>IP tiering
// in the generator) still resolves them deterministically — but they are usually
// a config mistake worth surfacing (e.g. one domain accidentally assigned to two
// services would silently follow whichever rule the generator emits first).
type Conflict struct {
	Kind     string   // "domain" | "rule_set" | "ip"
	Value    string   // the shared domain/tag, or "cidrA overlaps cidrB"
	Services []string // the services involved, sorted, deduplicated
}

func (c Conflict) String() string {
	return fmt.Sprintf("%s %q shared by %v", c.Kind, c.Value, c.Services)
}

// DetectConflicts reports overlaps across services' RuleSets, Domains and IPs.
// Exact-value collisions (a domain or rule-set tag claimed by 2+ services) and
// overlapping CIDRs are returned. Results are deterministic (sorted). A service
// overlapping only itself is never reported.
func DetectConflicts(services []Service) []Conflict {
	var out []Conflict
	out = append(out, exactCollisions("domain", domainOwners(services))...)
	out = append(out, exactCollisions("rule_set", ruleSetOwners(services))...)
	out = append(out, ipOverlaps(services)...)
	return out
}

// owners maps each value to the set of services that declare it.
func domainOwners(services []Service) map[string]map[string]bool {
	m := map[string]map[string]bool{}
	for _, s := range services {
		for _, d := range s.Domains {
			add(m, d, s.Name)
		}
	}
	return m
}

func ruleSetOwners(services []Service) map[string]map[string]bool {
	m := map[string]map[string]bool{}
	for _, s := range services {
		for _, rs := range s.RuleSets {
			add(m, rs, s.Name)
		}
	}
	return m
}

func add(m map[string]map[string]bool, key, svc string) {
	if m[key] == nil {
		m[key] = map[string]bool{}
	}
	m[key][svc] = true
}

// exactCollisions emits a Conflict for every value owned by >1 distinct service.
func exactCollisions(kind string, owners map[string]map[string]bool) []Conflict {
	var out []Conflict
	for value, svcs := range owners {
		if len(svcs) < 2 {
			continue
		}
		out = append(out, Conflict{Kind: kind, Value: value, Services: sortedKeys(svcs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// ipOverlaps reports pairs of services whose CIDRs intersect. Invalid CIDRs are
// skipped (config validation catches those separately).
func ipOverlaps(services []Service) []Conflict {
	type net4 struct {
		svc  string
		cidr string
		net  *net.IPNet
	}
	var nets []net4
	for _, s := range services {
		for _, c := range s.IPs {
			if _, n, err := net.ParseCIDR(c); err == nil {
				nets = append(nets, net4{svc: s.Name, cidr: c, net: n})
			}
		}
	}
	var out []Conflict
	for i := 0; i < len(nets); i++ {
		for j := i + 1; j < len(nets); j++ {
			a, b := nets[i], nets[j]
			if a.svc == b.svc {
				continue // a service overlapping itself is not a conflict
			}
			if a.net.Contains(b.net.IP) || b.net.Contains(a.net.IP) {
				svcs := sortedKeys(map[string]bool{a.svc: true, b.svc: true})
				out = append(out, Conflict{
					Kind:     "ip",
					Value:    a.cidr + " overlaps " + b.cidr,
					Services: svcs,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
