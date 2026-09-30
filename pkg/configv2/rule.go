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
	Behavior Behavior    `yaml:"behavior"`
	Chain    []ChainStep `yaml:"chain"`
	// Bypass says this rule does not TOUCH the traffic it matches: no route, no
	// desync, no probing, nothing. The traffic is left to the system default.
	//
	// It is a RULE flag and deliberately not a chain action, which is the whole of
	// §4. `direct` is a rung: the ladder tries paths until one works, and "no
	// proxy, no desync" is one of the paths worth trying. `bypass` is not a path —
	// it is the absence of a rule. Filed as an action it sat beside `desync` and
	// `vpn` in the same one-of list, so a rung could read as "escalate to bypassing",
	// and the UI had to render a non-path among the paths.
	//
	// It is NOT the same as `enabled: false`. Disabled is a switch the operator
	// turned off; bypass is a standing statement that these domains are not
	// Lotsman's business. They are carried separately for exactly that reason.
	Bypass bool `yaml:"bypass,omitempty"`
}

// Behavior is how this rule is STEERED, as opposed to what traffic it catches.
//
// These are v1's service-level knobs, kept as explicit rule fields because v2 has no
// behaviour profiles to inherit them through: a named profile would be exactly the
// indirection that made "which node is good for this" unanswerable from the config.
type Behavior struct {
	// Sticky keeps the rule on its current node while that node is healthy, and
	// fails over only on real failure. Unset is a THIRD state, not false: unset
	// takes the global default, so a rule that says nothing does not accidentally
	// opt into flapping. A challenger has to beat the incumbent by the switch
	// margin, so "sticky" means "slow to move", never "never move".
	Sticky *bool `yaml:"sticky"`
	// Profile is the traffic CLASS, not a set of weights: voice, gaming and
	// streaming want different things measured, and the knowledge base cannot
	// infer which of those a rule cares about. The weights stay internal to
	// pkg/balancer per class. A weight vector here would be a way to write down a
	// number that silently does nothing — nothing fills BandwidthMbps today
	// (LOT-69), so a `bandwidth:` term would drop out of the average and read as
	// a working setting.
	Profile string `yaml:"profile"`
	// SpreadClients are LAN client CIDRs to DISTRIBUTE across this rule's VPN-pool
	// nodes, so two devices using the same rule do not exit from the same IP. It
	// needs a pool with at least one node; with none, it is inert. Marginal for a
	// 2-5 device household (LOT-23).
	SpreadClients []string `yaml:"spread_clients"`
	// EscalateAfter and RecoverAfter are consecutive-probe thresholds: how many
	// failures before moving down the chain, and how many silent successes before
	// moving back. 0 means the global default, which is why these are plain ints
	// here and not *int — the zero already carries the right meaning.
	EscalateAfter int `yaml:"escalate_after"`
	RecoverAfter  int `yaml:"recover_after"`
	// TLSFragment emits a sing-box route tls_fragment for this rule, splitting
	// the TLS ClientHello across packets so the DPI cannot read the SNI from one
	// packet. It is a sing-box-native ALTERNATIVE to nfqws fragmentation, not a
	// zapret knob, and it is version-gated on sing-box >= 1.12. Per-pool
	// application is still open (ROUTING.md).
	TLSFragment bool `yaml:"tls_fragment"`
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

// Action is one rung of a chain. Exactly one field is set, and every field here is
// a PATH the ladder can try — which is the test `bypass` failed and moved out.
type Action struct {
	Desync *DesyncAction `yaml:"desync"`
	VPN    *VPNAction    `yaml:"vpn"`
	Direct bool          `yaml:"direct"`
}

type DesyncAction struct {
	Instance string `yaml:"instance"`
	Strategy string `yaml:"strategy"`
}

type VPNAction struct {
	Pool string `yaml:"pool"`
	// Detour wraps this rung's egress in ANOTHER pool: the traffic leaves through
	// pool A's node, which itself dials out through pool B's live node. That is
	// composition, not fallback — the chain is "try these until one works", and this
	// is "use A, but get there through B", arbitrarily deep.
	//
	// Cycles are refused at load (A→B→A): sing-box would accept the config and
	// resolve nothing, so the symptom would be traffic that simply never connects
	// while every status read reports the strategy applied.
	//
	// Measured on sing-box 1.14.1: `detour` is accepted on a node outbound and MAY
	// name a GROUP, so the underlay is itself a urltest and picks its own live
	// member. It is REFUSED on a urltest group ("unknown field"), which is why this
	// hangs on the nodes and not on the pool.
	Detour string `yaml:"detour,omitempty"`
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
