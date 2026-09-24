package singbox

// Split-DNS generation for the sing-box 1.12+ "new" DNS format (type-based servers).
// The shapes here are verified against the sing-box docs (dns/server/*, dns/rule,
// dns/rule_action, dns/server/fakeip) for the 1.12.x dual-accept window; the whole
// block is gated on FeatureDNSServers (>= 1.12.0) since a new-format config fails to
// LOAD on <= 1.11.
//
// The point of split-DNS for an anti-censorship client: resolve censored/default
// domains through a trusted encrypted resolver DETOURED THROUGH THE VPN (so the ISP
// neither sees the queries in the clear nor gets to poison the answers), while
// RU-direct domains resolve via a fast local/direct resolver (right geo, low latency).

// DNSOptions emits the dns block. nil => no dns block (sing-box falls back to its own
// default, which under a tun breaks resolution — so a client should always pass one).
type DNSOptions struct {
	Servers  []DNSServer    // upstreams, each pinned to an outbound via Detour
	Direct   string         // tag of the resolver for DirectRuleSets (usually a Detour:"direct"/local server); "" = no direct split
	Zapret   string         // tag of the resolver for zapret-class services' domains — a public resolver off-VPN; "" = no zapret split
	Final    string         // tag of the default/fallback resolver (censored + everything else)
	Strategy string         // prefer_ipv4 (default) | prefer_ipv6 | ipv4_only | ipv6_only
	FakeIP   *FakeIPOptions // optional: adds a fakeip server + an A/AAAA rule after the direct bypass
}

// DNSServer is one upstream. For an encrypted transport (tls/https/quic/h3) addressed
// by a HOSTNAME, set ServerName (SNI) and Bootstrap (a plain-IP/local server tag that
// resolves that hostname); for an IP literal, ServerName is the SNI to verify against
// (safer than relying on the provider shipping the IP in its cert SAN). Type "local"
// uses the OS resolver and takes no other field.
type DNSServer struct {
	Tag        string
	Type       string // udp | tcp | tls | https | quic | h3 | local
	Server     string // IP or hostname (empty for "local")
	Port       int    // 0 = per-type default (udp/tcp 53, tls/quic 853, https/h3 443)
	Path       string // DoH/DoH3 path ("" = /dns-query)
	ServerName string // TLS SNI (encrypted transports)
	Detour     string // outbound tag: "direct" or a VPN pool tag; "" = default dialer
	Bootstrap  string // domain_resolver server tag, for a hostname-addressed encrypted server
}

var dnsDefaultPort = map[string]int{"udp": 53, "tcp": 53, "tls": 853, "quic": 853, "https": 443, "h3": 443}

func dnsEncrypted(t string) bool {
	switch t {
	case "tls", "quic", "https", "h3":
		return true
	}
	return false
}

// dnsServerObject renders one server per the verified 1.12+ schema. validDetour
// reports whether an outbound tag exists; a detour naming a missing outbound (e.g. a
// VPN pool that never materialised because there are no nodes) would fail the config
// load, so it is dropped to the default dialer instead.
func dnsServerObject(s DNSServer, validDetour func(string) bool) map[string]any {
	if s.Type == "local" {
		return map[string]any{"tag": s.Tag, "type": "local"}
	}
	srv := map[string]any{"tag": s.Tag, "type": s.Type, "server": s.Server}
	port := s.Port
	if port == 0 {
		port = dnsDefaultPort[s.Type]
	}
	if port != 0 {
		srv["server_port"] = port
	}
	if dnsEncrypted(s.Type) {
		if s.Type == "https" || s.Type == "h3" {
			path := s.Path
			if path == "" {
				path = "/dns-query"
			}
			srv["path"] = path
		}
		if s.ServerName != "" {
			srv["tls"] = map[string]any{"server_name": s.ServerName}
		}
	}
	if s.Bootstrap != "" {
		srv["domain_resolver"] = s.Bootstrap
	}
	if s.Detour != "" && (s.Detour == "direct" || validDetour == nil || validDetour(s.Detour)) {
		srv["detour"] = s.Detour
	}
	return srv
}

// dnsSection builds the whole dns block. directRuleSets are the rule_set tags whose
// queries resolve via o.Direct (the RU-direct services). validDetour gates each
// server's detour against the outbounds that actually exist. Returns (nil,false) when
// there is nothing to emit or the target can't load the new DNS format.
func dnsSection(o *DNSOptions, directRuleSets, zapretRuleSets, zapretDomains []string, validDetour func(string) bool, caps Caps) (map[string]any, bool) {
	if o == nil || len(o.Servers) == 0 || !caps.Supports(FeatureDNSServers) {
		return nil, false
	}
	servers := make([]any, 0, len(o.Servers)+1)
	for _, s := range o.Servers {
		servers = append(servers, dnsServerObject(s, validDetour))
	}

	var rules []any
	// RU-direct domains resolve via the direct/local server (the office DNS), off the
	// VPN. Emitted FIRST so an office domain that also appears in a zapret list still
	// resolves internally.
	if o.Direct != "" && len(directRuleSets) > 0 {
		rules = append(rules, map[string]any{
			"rule_set": directRuleSets,
			"action":   "route",
			"server":   o.Direct,
		})
	}
	// Zapret-class services' domains resolve via a PUBLIC resolver off-VPN and off
	// the office DNS, so a desynced direct connection gets the real, unpoisoned IP
	// (LOT-83). Two rules because a sing-box rule ANDs its fields: one matches the
	// service rule-sets, one the inline domain suffixes.
	if o.Zapret != "" {
		if len(zapretRuleSets) > 0 {
			rules = append(rules, map[string]any{
				"rule_set": zapretRuleSets,
				"action":   "route",
				"server":   o.Zapret,
			})
		}
		if len(zapretDomains) > 0 {
			rules = append(rules, map[string]any{
				"domain_suffix": zapretDomains,
				"action":        "route",
				"server":        o.Zapret,
			})
		}
	}
	// Optional fakeip — a first-class server object in 1.12+, driven by an A/AAAA rule
	// placed AFTER the direct bypass so RU-direct still gets real IPs (rules match
	// top-down, first wins).
	if o.FakeIP != nil && caps.Supports(FeatureFakeIP) {
		in4, in6 := o.FakeIP.Inet4Range, o.FakeIP.Inet6Range
		if in4 == "" {
			in4 = "198.18.0.0/15"
		}
		if in6 == "" {
			in6 = "fc00::/18"
		}
		servers = append(servers, map[string]any{
			"tag": "dns_fakeip", "type": "fakeip", "inet4_range": in4, "inet6_range": in6,
		})
		rules = append(rules, map[string]any{
			"query_type": []string{"A", "AAAA"}, "action": "route", "server": "dns_fakeip",
		})
	}

	strategy := o.Strategy
	if strategy == "" {
		strategy = "prefer_ipv4"
	}
	dns := map[string]any{
		"servers":           servers,
		"final":             o.Final,
		"strategy":          strategy,
		"independent_cache": true,
	}
	if len(rules) > 0 {
		dns["rules"] = rules
	}
	return dns, true
}
