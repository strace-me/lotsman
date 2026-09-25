package configv2

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/coherence"
)

func TestCoherenceFlagsIPOnlyDesyncRule(t *testing.T) {
	rules := []Rule{{
		ID:      "example",
		Enabled: true,
		Match:   Match{IPLists: []string{"example"}},
		Chain:   []ChainStep{{Tier: "preferred", Action: Action{Desync: &DesyncAction{Instance: "nfqws1-main", Strategy: "alt12"}}}},
	}}
	gaps := Coherence(rules, nil, map[string]ListShape{"example": {Empty: true}})
	if len(gaps) != 2 {
		t.Fatalf("gaps = %#v", gaps)
	}
	if gaps[0].Kind != coherence.GapIPZapret {
		t.Errorf("gaps = %#v", gaps)
	}
}

func TestCoherenceIgnoresNonDesyncRules(t *testing.T) {
	rules := []Rule{{
		ID:      "example",
		Enabled: true,
		Match:   Match{CIDRs: []string{"10.0.0.0/24"}},
		Chain:   []ChainStep{{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "vpn"}}}},
	}}
	if gaps := Coherence(rules, nil, nil); len(gaps) != 0 {
		t.Fatalf("gaps = %#v", gaps)
	}
}

func TestCoherenceFlagsEmptyDomainList(t *testing.T) {
	rules := []Rule{{
		ID:      "example",
		Enabled: true,
		Match:   Match{DomainLists: []string{"example"}},
		Chain:   []ChainStep{{Tier: "preferred", Action: Action{Desync: &DesyncAction{Instance: "nfqws1-main", Strategy: "alt12"}}}},
	}}
	gaps := Coherence(rules, map[string]ListShape{"example": {Empty: true}}, nil)
	if len(gaps) != 1 || gaps[0].Kind != coherence.GapRuleSetZapret {
		t.Fatalf("gaps = %#v", gaps)
	}
	if DescribeGaps(gaps) == "" {
		t.Error("gaps were not described")
	}
}
