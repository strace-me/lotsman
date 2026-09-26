package configv2

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/coherence"
)

func desyncRule(id string) Rule {
	return Rule{
		ID:      id,
		Enabled: true,
		Chain:   []ChainStep{{Tier: "preferred", Action: Action{Desync: &DesyncAction{Instance: "nfqws1-main", Strategy: "alt12"}}}},
	}
}

func TestCoherenceFlagsAnyIPMatchOnADesyncRule(t *testing.T) {
	// The list is NOT empty, and the gap must still be reported: nfqws matches the
	// SNI hostname, so a packet matched by CIDR is never desynced. Reporting only
	// empty lists (the earlier bug) made a real hole read as "no gaps".
	rule := desyncRule("discord")
	rule.Match = Match{DomainLists: []string{"discord"}, IPLists: []string{"voice"}}
	gaps := Coherence([]Rule{rule}, map[string]ListShape{"discord": {}}, map[string]ListShape{"voice": {}})
	if len(gaps) != 1 || gaps[0].Kind != coherence.GapIPZapret || gaps[0].Detail != "voice" {
		t.Fatalf("gaps = %#v", gaps)
	}
}

func TestCoherenceFlagsEmptyIPListDistinctly(t *testing.T) {
	rule := desyncRule("discord")
	rule.Match = Match{DomainLists: []string{"discord"}, IPLists: []string{"voice"}}
	gaps := Coherence([]Rule{rule}, map[string]ListShape{"discord": {}}, map[string]ListShape{"voice": {Empty: true}})
	if len(gaps) != 1 || gaps[0].Detail != "voice (empty)" {
		t.Fatalf("gaps = %#v", gaps)
	}
}

func TestCoherenceFlagsInlineCIDRsOnADesyncRule(t *testing.T) {
	rule := desyncRule("discord")
	rule.Match = Match{Domains: []string{"discord.com"}, CIDRs: []string{"203.0.113.10/18"}}
	if gaps := Coherence([]Rule{rule}, nil, nil); len(gaps) != 1 {
		t.Fatalf("gaps = %#v", gaps)
	}
}

func TestCoherenceIsQuietForADomainOnlyDesyncRule(t *testing.T) {
	rule := desyncRule("youtube")
	rule.Match = Match{DomainLists: []string{"youtube"}}
	if gaps := Coherence([]Rule{rule}, map[string]ListShape{"youtube": {}}, nil); len(gaps) != 0 {
		t.Fatalf("gaps = %#v", gaps)
	}
}

func TestCoherenceFlagsAnEmptyDomainList(t *testing.T) {
	rule := desyncRule("youtube")
	rule.Match = Match{DomainLists: []string{"youtube"}}
	gaps := Coherence([]Rule{rule}, map[string]ListShape{"youtube": {Empty: true}}, nil)
	if len(gaps) != 1 || gaps[0].Kind != coherence.GapRuleSetZapret {
		t.Fatalf("gaps = %#v", gaps)
	}
}
