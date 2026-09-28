package configv2

import (
	"fmt"
	"regexp"
)

// The enum tables below are the single source of truth: validation checks
// against them, and the GUI/LuCI metadata endpoint serves them, so a UI can
// never offer a value the loader would reject.
var (
	ProxyEngines  = []string{"sing-box"}
	ProxyModes    = []string{"tun", "proxy"}
	DesyncEngines = []string{"nfqws1", "nfqws2"}
	ProbeTypes    = []string{"http", "tcp", "stun", "quic"}
	ChainTiers    = []string{"preferred", "fallback", "emergency"}
	// TrafficClasses is the `profile` vocabulary: a rule names what class of
	// traffic it carries, and pkg/balancer holds the per-class weighting
	// internally. Exposing the weights instead would let a config name a term
	// nothing measures (LOT-69: BandwidthMbps is filled by no one), and the term
	// would leave the average while the number still read as a live setting.
	TrafficClasses = []string{"general", "voice", "streaming", "gaming"}
)

// nftTablePattern is one family/table pair, the form `table <family> <name>`
// takes. Anything looser would be rejected by nft at install time, on a box
// where the failure is "desync silently absent".
var nftTablePattern = regexp.MustCompile(`^(inet|ip|ip6|bridge|netdev|mangle) [A-Za-z0-9_.-]+$`)

type ChainActionMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"`
}

var ChainActions = []ChainActionMeta{
	{Name: "desync", Description: "desync engine + strategy bundle", Fields: []string{"instance", "strategy"}},
	{Name: "vpn", Description: "proxy pool from sing-box", Fields: []string{"pool"}},
	{Name: "direct", Description: "no proxy, no desync", Fields: nil},
	{Name: "bypass", Description: "exclude from this rule entirely", Fields: nil},
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ValidateBehavior checks the behaviour block against the enum table, so an
// unknown class is a load error rather than a class the balancer silently
// treats as general.
func (b Behavior) Validate(ruleID string) error {
	if b.Profile != "" && !contains(TrafficClasses, b.Profile) {
		return fmt.Errorf("rule %q: unknown traffic class %q (have %v)", ruleID, b.Profile, TrafficClasses)
	}
	return nil
}
