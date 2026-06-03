// Package registry holds services, their categories, and the denormalized
// strategy chain (STATE_CHAINS). M0 hardcodes one service (youtube); a later
// phase loads this from YAML.
package registry

import "github.com/strace-me/lotsman/pkg/strategy"

// Chain states. State is derived from a service's position in its chain.
const (
	StatePreferred = "PREFERRED"
	StateAltZapret = "ALT_ZAPRET"
	StateVPN       = "VPN"
	StateEmergency = "EMERGENCY"
	StateBroken    = "BROKEN"
	StateLocked    = "LOCKED"
)

// ChainStep is one position in a service's fallback chain. StrategyID may be
// empty when the step's strategy is chosen at runtime from the KB (e.g. the
// ALT_ZAPRET step asks the KB for the next-best zapret strategy).
type ChainStep struct {
	Position      int
	State         string
	StrategyClass string
	StrategyID    string // empty => resolve from KB at escalation time
}

// Service is a registered service and its denormalized chain. The route-match
// inputs (RuleSets/Domains/IPs) name the traffic this service owns; the
// generator turns them into sing-box route rules pointing at the service
// selector. IPs are the geoip replacement for UDP/voice paths that carry no SNI
// (e.g. Discord voice servers).
type Service struct {
	Name          string
	Category      string
	ProbeType     string   // http | tcp | stun (empty = http)
	ProbeTarget   string   // http: URL; tcp/stun: host:port
	RuleSets      []string // sing-box rule-set tags (big curated .srs lists)
	Domains       []string // inline domain_suffix matches (custom/small lists)
	IPs           []string // inline ip_cidr matches (canonical CIDR; voice/geoip)
	Sticky        bool     // keep this service pinned to one node; fail over only on real failure, never flap by latency
	Profile       string   // node-quality weighting: "" | general | voice | streaming | gaming
	Static        bool     // nailed: the daemon never escalates/recovers this service (use the configured chain[0] as-is)
	EscalateAfter int      // per-service consecutive-fail threshold before escalating (0 = global default)
	RecoverAfter  int      // per-service consecutive silent-success threshold before recovering (0 = global default)
	Priority      int      // route-rule order: LOWER is emitted earlier (sing-box = first match wins). Negative for protective/specific rules (ru-direct), positive for broad catch-alls (ru-blocked). Default 0.
	TLSFragment   bool     // emit sing-box route tls_fragment for this service: splits the TLS ClientHello across packets so DPI cannot read the SNI in one packet (a sing-box-native alternative to nfqws fragmentation). Off by default.
	Chain         []ChainStep
}

// DirectOnly reports whether the service is hard-pinned to direct (the ru_direct
// / LOCKED case): no VPN step, and its first chain step is the direct class. Such
// a service must route straight to the "direct" outbound, never a flippable
// selector, so RU traffic can never leak to a VPN exit.
func (s Service) DirectOnly() bool {
	if s.VPNPool() != "" {
		return false
	}
	return len(s.Chain) > 0 && s.Chain[0].StrategyClass == strategy.ClassDirect
}

// Category defines the default behavior a service inherits. A service declaring
// `category: streaming` and no explicit chain uses streaming's DefaultChain.
// RequiredCaps documents what its VPN pool must carry (e.g. messaging needs
// udp_native for voice) — load-bearing once the generator picks pools by caps.
type Category struct {
	Name         string
	RequiredCaps []string    // "tcp" | "udp_native"
	DefaultChain []ChainStep // inherited when the service declares no chain (Position assigned at build)
}

// BuiltinCategories returns the seven categories from the spec (§4.1.1/4.1.2).
// Config may override or add categories by name. Zapret steps leave StrategyID
// empty so the KB resolves the best-known strategy at runtime (bounded to the
// catalog); VPN/emergency steps name a pool.
func BuiltinCategories() map[string]Category {
	zapretToVPN := func(vpnPool string) []ChainStep {
		return []ChainStep{
			{State: StatePreferred, StrategyClass: strategy.ClassZapret},
			{State: StateAltZapret, StrategyClass: strategy.ClassZapret},
			{State: StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: vpnPool},
			{State: StateEmergency, StrategyClass: strategy.ClassEmergency, StrategyID: "emergency_pool"},
		}
	}
	return map[string]Category{
		"ru_direct": {Name: "ru_direct", RequiredCaps: []string{"tcp"}, DefaultChain: []ChainStep{
			{State: StateLocked, StrategyClass: strategy.ClassDirect, StrategyID: "direct"},
		}},
		"streaming": {Name: "streaming", RequiredCaps: []string{"tcp"}, DefaultChain: zapretToVPN("vpn_url_test")},
		"messaging": {Name: "messaging", RequiredCaps: []string{"tcp", "udp_native"}, DefaultChain: zapretToVPN("vpn_url_test_udp")},
		"gaming":    {Name: "gaming", RequiredCaps: []string{"tcp", "udp_native"}, DefaultChain: zapretToVPN("vpn_url_test_udp")},
		"dev_tools": {Name: "dev_tools", RequiredCaps: []string{"tcp"}, DefaultChain: zapretToVPN("vpn_url_test")},
		"iot_check": {Name: "iot_check", RequiredCaps: []string{"tcp"}, DefaultChain: zapretToVPN("vpn_url_test")},
		"generic":   {Name: "generic", RequiredCaps: []string{"tcp"}, DefaultChain: zapretToVPN("vpn_url_test")},
	}
}

// Device is a per-source routing override: traffic from these LAN sources is
// routed by the device's policy regardless of destination (e.g. the gaming PC
// always direct, a kid's tablet always via VPN). Source matching is by IP/CIDR
// (reliable with DHCP reservations).
type Device struct {
	Name    string
	Sources []string // IP or CIDR, e.g. "192.168.1.50" / "192.168.1.0/28"
	Policy  string   // "direct" | "block" | a pool/selector name (e.g. "vpn_url_test")
}

// SelectorTag is the sing-box selector that routes a service's traffic. The
// generator emits it and the executors flip it, so the naming convention lives
// here to keep the two in agreement.
func SelectorTag(service string) string { return "sel-" + service }

// VPNPool returns the pool a service's VPN step selects, or "" if it has none.
func (s Service) VPNPool() string {
	for _, step := range s.Chain {
		if step.StrategyClass == strategy.ClassVPN {
			return step.StrategyID
		}
	}
	return ""
}

// Registry is the set of known services keyed by name.
type Registry struct {
	Services map[string]Service
}

// Builtin returns the M0 registry: youtube only.
//
// Chain exercises all three M0 mechanisms:
//   - PREFERRED  zapret, fixed seed strategy (static nfqws instance)
//   - ALT_ZAPRET zapret, StrategyID empty => resolved from KB.TopNZapret
//   - VPN        url-test pool via Clash API
func Builtin() *Registry {
	return &Registry{Services: map[string]Service{
		"youtube": {
			Name:        "youtube",
			Category:    "streaming",
			ProbeTarget: "https://www.youtube.com/generate_204",
			Chain: []ChainStep{
				{Position: 0, State: StatePreferred, StrategyClass: strategy.ClassZapret, StrategyID: strategy.BuiltinZapretSeed[0]},
				{Position: 1, State: StateAltZapret, StrategyClass: strategy.ClassZapret, StrategyID: ""},
				{Position: 2, State: StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		},
	}}
}
