package configv2

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type Rule struct {
	ID       string      `yaml:"id"`
	Name     string      `yaml:"name"`
	Enabled  bool        `yaml:"enabled"`
	Priority int         `yaml:"priority"`
	Match    Match       `yaml:"match"`
	Probe    *Probe      `yaml:"probe"`
	Volume   *Volume     `yaml:"volume"`
	Chain    []ChainStep `yaml:"chain"`
}

type Match struct {
	DomainLists []string `yaml:"domain_lists"`
	Domains     []string `yaml:"domains"`
	IPLists     []string `yaml:"ip_lists"`
	CIDRs       []string `yaml:"cidrs"`
}

type Probe struct {
	Type   string `yaml:"type"`
	Target string `yaml:"target"`
}

type Volume struct {
	Targets  []string `yaml:"targets"`
	MinBytes int64    `yaml:"min_bytes"`
}

// AutoStrategy is the desync strategy value that defers to the knowledge base:
// this rung uses whatever has been proven to work here, so no bundle file carries
// the name. v1 spelled it as an empty strategy_id on an ALT_ZAPRET rung, which
// read as pinned to whatever happened to be selected.
const AutoStrategy = "auto"

// ChainStep is one rung: an action, optionally labelled with the tier it belongs
// to. The LIST order is the escalation order — that is the whole semantics, and
// it is unbounded. A tier is a label for the UI and the status line, not a slot:
// the ladder an operator writes is routinely preferred×2, fallback×2, emergency,
// because a desync rung can name a pinned strategy and then fall back to whatever
// the KB prefers, and a tier can carry a UDP-native pool before a TCP one.
type ChainStep struct {
	Tier   string `yaml:"tier"`
	Action Action `yaml:",inline"`
}

type Action struct {
	Desync *DesyncAction `yaml:"desync"`
	VPN    *VPNAction    `yaml:"vpn"`
	Direct bool          `yaml:"direct"`
	Bypass bool          `yaml:"bypass"`
}

type DesyncAction struct {
	Instance string `yaml:"instance"`
	Strategy string `yaml:"strategy"`
}

type VPNAction struct {
	Pool string `yaml:"pool"`
}

// Unmarshalling is hand-rolled because the step is a flat mapping with an
// optional `tier` key plus exactly one action key, which a struct tag cannot
// express.
func (s *ChainStep) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("chain step must be a mapping")
	}
	var action Action
	if err := node.Decode(&action); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "tier" {
			continue
		}
		tier := node.Content[i+1].Value
		if tier != "" && !contains(ChainTiers, tier) {
			return fmt.Errorf("unknown chain tier %q", tier)
		}
		s.Tier = tier
	}
	s.Action = action
	return nil
}
