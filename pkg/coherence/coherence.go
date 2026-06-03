// Package coherence reconciles the three differently-shaped config models
// Lotsman drives from one source of truth (the service registry):
//
//   - sing-box ROUTING — matches by domain_suffix / rule_set(.srs) / ip_cidr and
//     changes the PATH (tunnel to a foreign outbound). Owned by pkg/singbox.
//   - nfqws/zapret DESYNC — matches by SNI hostname (+ port + L7) and mangles
//     PACKETS on the direct path. Fed by a plaintext hostlist.
//
// A service routed to zapret is sent DIRECT by sing-box on the assumption that
// nfqws will desync its traffic. That only holds if the service's domains are in
// the nfqws hostlist — and nfqws can only match plaintext HOSTNAMES, not the
// rule_set(.srs) blobs or ip_cidr ranges sing-box also routes on. Analyze closes
// that loop (projecting zapret-service domains into the hostlist) and reports the
// gaps it cannot close, turning silent failures into loud warnings.
package coherence

import (
	"sort"
	"strings"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// Gap kinds — a coherence hole between the routing model and the desync model.
const (
	// GapRuleSetZapret: a zapret service matches by rule_set(.srs). nfqws cannot
	// read the binary rule-set, so those domains get routed direct but never
	// desynced — a silent hole if any of them is DPI-blocked.
	GapRuleSetZapret = "ruleset_zapret"
	// GapIPZapret: a zapret service matches by ip_cidr. nfqws matches the SNI
	// hostname, which a bare-IP packet does not carry, so those matches cannot be
	// desynced by the hostlist (they may be handled by a separate nfqws L7/port
	// rule, or not at all).
	GapIPZapret = "ip_zapret"
	// GapExcludeOverlap: a domain we would feed to nfqws is also in the nfqws
	// exclude list, so nfqws will skip it — routed direct, never desynced.
	GapExcludeOverlap = "exclude_overlap"
)

// Gap is one coherence hole, attributed to a service.
type Gap struct {
	Service string
	Kind    string
	Detail  string
}

// EngineFacts carries what the neighbouring engines actually do, read from the
// live box by the caller (the pure Analyze takes them as input).
type EngineFacts struct {
	// NfqwsExcludeDomains are domains in the nfqws exclude list(s), lower-cased.
	NfqwsExcludeDomains map[string]bool
}

// Plan is the projection of the registry into the nfqws hostlist plus the
// coherence gaps that could not be projected.
type Plan struct {
	NfqwsDomains []string // plaintext domains to feed nfqws (deduped, sorted, exclude-overlaps removed)
	Gaps         []Gap
}

// hasZapretStep reports whether any step in the service's chain is zapret-class
// (so the service may be routed direct and rely on nfqws desync).
func hasZapretStep(s registry.Service) bool {
	for _, st := range s.Chain {
		if st.StrategyClass == strategy.ClassZapret {
			return true
		}
	}
	return false
}

// Analyze projects the zapret-class services into an nfqws hostlist and reports
// the coherence gaps between the routing and desync models. Pure: no I/O.
func Analyze(services []registry.Service, facts EngineFacts) Plan {
	seen := map[string]bool{}
	var domains []string
	var gaps []Gap

	for _, s := range services {
		if !hasZapretStep(s) {
			continue // not routed to zapret; nfqws coverage is irrelevant for it
		}

		// Plaintext domains are the only thing nfqws can match on — feed them,
		// minus any the nfqws exclude list would drop anyway.
		for _, d := range s.Domains {
			d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "*.")))
			if d == "" {
				continue
			}
			if facts.NfqwsExcludeDomains[d] {
				gaps = append(gaps, Gap{Service: s.Name, Kind: GapExcludeOverlap, Detail: d})
				continue
			}
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}

		// rule_set / ip_cidr matches on a zapret service cannot be projected to
		// the hostname-based hostlist — report them so the hole is visible.
		if len(s.RuleSets) > 0 {
			gaps = append(gaps, Gap{Service: s.Name, Kind: GapRuleSetZapret, Detail: strings.Join(s.RuleSets, ",")})
		}
		if len(s.IPs) > 0 {
			gaps = append(gaps, Gap{Service: s.Name, Kind: GapIPZapret, Detail: strings.Join(s.IPs, ",")})
		}
	}

	sort.Strings(domains)
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Service != gaps[j].Service {
			return gaps[i].Service < gaps[j].Service
		}
		return gaps[i].Kind < gaps[j].Kind
	})
	return Plan{NfqwsDomains: domains, Gaps: gaps}
}
