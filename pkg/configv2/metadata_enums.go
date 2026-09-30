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
	// DataSourceTypes is the vocabulary of `data_sources[].type`, from the same table
	// the validator and the GUI read.
	DataSourceTypes = []string{"v2ray-srs", "srs", "plain"}
)

// nftTablePattern is one family/table pair, the form `table <family> <name>`
// takes. Anything looser would be rejected by nft at install time, on a box
// where the failure is "desync silently absent".
var nftTablePattern = regexp.MustCompile(`^(inet|ip|ip6|bridge|netdev|mangle) [A-Za-z0-9_.-]+$`)

// iso2 is an ISO-3166 alpha-2 country code, lower-cased. A pool filter that says
// "de" must not be able to say "germany" or "DE" and quietly match nothing: the
// exclude would look applied while removing nothing.
var iso2 = regexp.MustCompile(`^[a-z]{2}$`)

type FlagMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ChainActionMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"`
}

var ChainActions = []ChainActionMeta{
	{Name: "desync", Description: "desync engine + strategy bundle", Fields: []string{"instance", "strategy"}},
	{Name: "vpn", Description: "proxy pool from sing-box", Fields: []string{"pool"}},
	{Name: "direct", Description: "no proxy, no desync", Fields: nil},
}

// RuleFlags are the RULE-level switches, kept apart from the chain actions on
// purpose (§4). A UI renders these in the section form and the actions in the
// ladder list, because they are different levels: an action is a path the ladder
// can try, a flag is a statement about the rule itself. Filing them together is
// what let a rung be "bypass" — an escalation to the absence of a rule.
var RuleFlags = []FlagMeta{
	{Name: "bypass", Description: "this rule does not touch the traffic it matches: no route, no desync, no probing. Not the same as enabled:false, which is a switch the operator turned off"},
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
