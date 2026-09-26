package configv2

import (
	"fmt"
	"sort"
	"strings"

	"github.com/strace-me/lotsman/pkg/coherence"
)

// Coherence reports the holes a v2 rule set leaves between the routing model
// and a desync engine: nfqws matches the SNI hostname, so a rule that routes
// only by IP/CIDR, or that leans on a list the hostlist cannot carry, silently
// gets its traffic sent direct without desync.
func Coherence(rules []Rule, domains, ips map[string]ListShape) []coherence.Gap {
	var gaps []coherence.Gap
	for _, rule := range rules {
		if !rule.Enabled || !usesDesync(rule) {
			continue
		}
		// An IP match on a desync rule is a gap WHENEVER it exists, not only when
		// the list is empty: nfqws matches the SNI hostname, and a bare-IP packet
		// carries none, so those matches are never desynced either way. Only a
		// non-empty list even has something behind it.
		for _, name := range rule.Match.IPLists {
			shape, known := ips[name]
			detail := name
			switch {
			case !known:
				detail += " (unknown ip list)"
			case shape.Empty:
				detail += " (empty)"
			}
			gaps = append(gaps, coherence.Gap{Service: rule.ID, Kind: coherence.GapIPZapret, Detail: detail})
		}
		if len(rule.Match.CIDRs) > 0 {
			gaps = append(gaps, coherence.Gap{Service: rule.ID, Kind: coherence.GapIPZapret, Detail: strings.Join(rule.Match.CIDRs, ",")})
		}
		if len(rule.Match.DomainLists) == 0 && len(rule.Match.Domains) == 0 {
			gaps = append(gaps, coherence.Gap{Service: rule.ID, Kind: coherence.GapIPZapret, Detail: "rule has no domain match at all"})
		}
		for _, name := range rule.Match.DomainLists {
			if shape, ok := domains[name]; ok && shape.Empty {
				gaps = append(gaps, coherence.Gap{Service: rule.ID, Kind: coherence.GapRuleSetZapret, Detail: name + " (empty)"})
			}
		}
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Service != gaps[j].Service {
			return gaps[i].Service < gaps[j].Service
		}
		return gaps[i].Kind < gaps[j].Kind
	})
	return gaps
}

// ListShape reports what the hostlist actually got for a named list.
type ListShape struct{ Empty bool }

func usesDesync(rule Rule) bool {
	for _, step := range rule.Chain {
		if step.Action.Desync != nil {
			return true
		}
	}
	return false
}

func DescribeGaps(gaps []coherence.Gap) string {
	if len(gaps) == 0 {
		return "no coherence gaps"
	}
	parts := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		parts = append(parts, fmt.Sprintf("%s: %s (%s)", gap.Service, gap.Kind, gap.Detail))
	}
	return strings.Join(parts, "; ")
}
