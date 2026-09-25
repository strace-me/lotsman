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

type ChainStep struct {
	Tier   string
	Action Action
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

func (s *ChainStep) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return fmt.Errorf("chain step must be a single tier mapping")
	}
	tier := node.Content[0].Value
	if !contains(ChainTiers, tier) {
		return fmt.Errorf("unknown chain tier %q", tier)
	}
	var action Action
	if err := node.Content[1].Decode(&action); err != nil {
		return err
	}
	s.Tier, s.Action = tier, action
	return nil
}
