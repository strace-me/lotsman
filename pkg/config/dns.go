package config

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// DNS is the resolved, validated split-DNS configuration. Present => it drives the
// generated sing-box dns block; nil => the client uses its built-in default
// (Cloudflare DoH detoured through the VPN + the OS resolver).
type DNS struct {
	Servers  []DNSServer
	Direct   string // tag of the resolver for RU-direct domains ("" = no direct split)
	Final    string // tag of the default/fallback resolver
	Strategy string // prefer_ipv4 | prefer_ipv6 | ipv4_only | ipv6_only ("" = prefer_ipv4)
	FakeIP   bool
}

// DNSServer is one upstream, already expanded from any provider alias to concrete
// transport details. Detour is "vpn" | "direct" | a pool tag; Bootstrap marks a
// hostname-addressed encrypted server that needs a bootstrap resolver (curated
// providers are IP-addressed with an explicit SNI, so they never need one).
type DNSServer struct {
	Name       string
	Type       string // udp | tcp | tls | https | quic | h3 | local
	Address    string
	Port       int
	Path       string
	ServerName string
	Detour     string
	Bootstrap  bool
}

// DNSProvider is a curated public resolver: its verifying hostname (TLS SNI / DoH
// host) and a stable anycast IP. A config says {provider: cloudflare, method: tls}
// instead of pinning endpoints by hand. Addressed by IP with an explicit SNI, so the
// bootstrap lookup can't be poisoned and a tunnel-detoured query is unblockable.
// Method is NOT restricted per provider — an unsupported pick simply fails at connect
// time, so switch transports; this keeps the catalog free of a fragile support matrix.
type DNSProvider struct {
	Host string // TLS server_name / DoH host
	IP   string // primary anycast IP literal (verified reachable + cert-valid)
	Path string // DoH path if non-standard ("" = /dns-query)
}

// dnsProviders — the IP+host pairs were verified live (TLS handshake to the IP with
// the host as SNI succeeded) so the encoded endpoints are real, not guessed.
var dnsProviders = map[string]DNSProvider{
	"cloudflare": {Host: "cloudflare-dns.com", IP: "1.1.1.1"},
	"quad9":      {Host: "dns.quad9.net", IP: "9.9.9.9"},
	"google":     {Host: "dns.google", IP: "8.8.8.8"},
	"adguard":    {Host: "dns.adguard-dns.com", IP: "94.140.14.14"},
	"mullvad":    {Host: "dns.mullvad.net", IP: "194.242.2.2"},
}

func knownProviders() string {
	ps := make([]string, 0, len(dnsProviders))
	for p := range dnsProviders {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return strings.Join(ps, ", ")
}

var dnsTypes = map[string]bool{"udp": true, "tcp": true, "tls": true, "https": true, "quic": true, "h3": true, "local": true}

func dnsEncryptedType(t string) bool {
	switch t {
	case "tls", "https", "quic", "h3":
		return true
	}
	return false
}

// --- on-disk shape ---

type dnsYAML struct {
	Servers  []dnsServerYAML `yaml:"servers"`
	Direct   string          `yaml:"direct"`
	Final    string          `yaml:"final"`
	Strategy string          `yaml:"strategy"`
	FakeIP   bool            `yaml:"fakeip"`
}

type dnsServerYAML struct {
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"` // curated alias: fills address + server_name
	Method   string `yaml:"method"`   // with provider: https(default)|tls|quic|h3|tcp|udp
	// manual form (no provider):
	Type       string `yaml:"type"` // udp|tcp|tls|https|quic|h3|local
	Address    string `yaml:"address"`
	Port       int    `yaml:"port"`
	Path       string `yaml:"path"`
	ServerName string `yaml:"server_name"`
	Detour     string `yaml:"detour"` // vpn(default for remote)|direct|a pool tag
}

// buildDNS translates + validates the on-disk dns block, expanding provider aliases.
func buildDNS(y *dnsYAML) (*DNS, error) {
	if y == nil || len(y.Servers) == 0 {
		return nil, nil
	}
	d := &DNS{Direct: y.Direct, Final: y.Final, Strategy: y.Strategy, FakeIP: y.FakeIP}
	seen := map[string]bool{}
	for _, s := range y.Servers {
		if s.Name == "" {
			return nil, fmt.Errorf("config: dns server with empty name")
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("config: duplicate dns server name %q", s.Name)
		}
		seen[s.Name] = true
		srv, err := resolveDNSServer(s)
		if err != nil {
			return nil, err
		}
		d.Servers = append(d.Servers, srv)
	}
	if d.Strategy != "" {
		switch d.Strategy {
		case "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only":
		default:
			return nil, fmt.Errorf("config: dns.strategy %q must be prefer_ipv4|prefer_ipv6|ipv4_only|ipv6_only", d.Strategy)
		}
	}
	if d.Final == "" || !seen[d.Final] {
		return nil, fmt.Errorf("config: dns.final must name one of the declared servers")
	}
	if d.Direct != "" && !seen[d.Direct] {
		return nil, fmt.Errorf("config: dns.direct %q is not a declared server", d.Direct)
	}
	return d, nil
}

func resolveDNSServer(s dnsServerYAML) (DNSServer, error) {
	out := DNSServer{Name: s.Name, Port: s.Port, Detour: s.Detour}
	if s.Provider != "" {
		p, ok := dnsProviders[s.Provider]
		if !ok {
			return out, fmt.Errorf("config: dns server %q: unknown provider %q (known: %s)", s.Name, s.Provider, knownProviders())
		}
		method := s.Method
		if method == "" {
			method = "https"
		}
		if !dnsEncryptedType(method) && method != "udp" && method != "tcp" {
			return out, fmt.Errorf("config: dns server %q: bad method %q (https|tls|quic|h3|tcp|udp)", s.Name, method)
		}
		out.Type = method
		out.Address = p.IP // IP + explicit SNI: unblockable, no bootstrap to poison
		out.ServerName = p.Host
		out.Path = p.Path
		return out, nil
	}
	if !dnsTypes[s.Type] {
		return out, fmt.Errorf("config: dns server %q: needs a provider or a valid type (udp|tcp|tls|https|quic|h3|local)", s.Name)
	}
	out.Type = s.Type
	if s.Type == "local" {
		return out, nil // local uses the OS resolver; no address/detour
	}
	if s.Address == "" {
		return out, fmt.Errorf("config: dns server %q: %s needs an address", s.Name, s.Type)
	}
	out.Address = s.Address
	out.Path = s.Path
	out.ServerName = s.ServerName
	if dnsEncryptedType(s.Type) && net.ParseIP(s.Address) == nil {
		// hostname-addressed encrypted server: SNI defaults to the hostname and it
		// must be bootstrapped by another resolver.
		if out.ServerName == "" {
			out.ServerName = s.Address
		}
		out.Bootstrap = true
	}
	return out, nil
}
