package config

import "testing"

func TestBuildDNSExpandsProvidersAndValidates(t *testing.T) {
	y := &dnsYAML{
		Servers: []dnsServerYAML{
			{Name: "cf-dot", Provider: "cloudflare", Method: "tls", Detour: "vpn"},
			{Name: "quad9", Provider: "quad9"}, // method defaults to https
			{Name: "lan", Type: "local"},
			{Name: "custom", Type: "quic", Address: "dns.example.net", Detour: "vpn"}, // hostname => bootstrap
		},
		Direct: "lan", Final: "cf-dot", Strategy: "prefer_ipv4",
	}
	d, err := buildDNS(y)
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	if len(d.Servers) != 4 || d.Final != "cf-dot" || d.Direct != "lan" {
		t.Fatalf("top-level wrong: %+v", d)
	}
	cf := d.Servers[0]
	if cf.Type != "tls" || cf.Address != "1.1.1.1" || cf.ServerName != "cloudflare-dns.com" || cf.Detour != "vpn" {
		t.Errorf("cloudflare DoT not expanded from catalog: %+v", cf)
	}
	if q := d.Servers[1]; q.Type != "https" || q.Address != "9.9.9.9" || q.ServerName != "dns.quad9.net" {
		t.Errorf("quad9 default-method (https) expansion wrong: %+v", q)
	}
	if l := d.Servers[2]; l.Type != "local" || l.Address != "" {
		t.Errorf("local server must carry no address: %+v", l)
	}
	if c := d.Servers[3]; !c.Bootstrap || c.ServerName != "dns.example.net" {
		t.Errorf("hostname-addressed encrypted server must set SNI + need a bootstrap: %+v", c)
	}
}

func TestBuildDNSRejectsBadConfigs(t *testing.T) {
	cases := map[string]*dnsYAML{
		"unknown provider":    {Servers: []dnsServerYAML{{Name: "x", Provider: "nope"}}, Final: "x"},
		"final not declared":  {Servers: []dnsServerYAML{{Name: "a", Type: "local"}}, Final: "b"},
		"bad strategy":        {Servers: []dnsServerYAML{{Name: "a", Type: "local"}}, Final: "a", Strategy: "fastest"},
		"duplicate name":      {Servers: []dnsServerYAML{{Name: "a", Type: "local"}, {Name: "a", Type: "local"}}, Final: "a"},
		"no type no provider": {Servers: []dnsServerYAML{{Name: "a"}}, Final: "a"},
		"direct not declared": {Servers: []dnsServerYAML{{Name: "a", Type: "local"}}, Final: "a", Direct: "z"},
	}
	for name, y := range cases {
		if _, err := buildDNS(y); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestBuildDNSFailoverValidatesAndRecordsProvider(t *testing.T) {
	y := &dnsYAML{
		Servers: []dnsServerYAML{
			{Name: "remote", Provider: "cloudflare", Method: "https", Detour: "vpn"},
			{Name: "lan", Type: "local"},
		},
		Direct: "lan", Final: "remote",
		Failover: []string{"cloudflare", "quad9", "google"},
	}
	d, err := buildDNS(y)
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	if len(d.Failover) != 3 || d.Failover[1] != "quad9" {
		t.Errorf("failover list not carried through: %v", d.Failover)
	}
	if d.Servers[0].Provider != "cloudflare" {
		t.Errorf("provider alias must be recorded on the resolved server, got %q", d.Servers[0].Provider)
	}
}

func TestBuildDNSFailoverRejectsBadConfigs(t *testing.T) {
	cases := map[string]*dnsYAML{
		"unknown failover provider": {
			Servers:  []dnsServerYAML{{Name: "remote", Provider: "cloudflare"}, {Name: "lan", Type: "local"}},
			Final:    "remote",
			Failover: []string{"cloudflare", "nope"},
		},
		"failover with manual final": {
			Servers:  []dnsServerYAML{{Name: "remote", Type: "https", Address: "1.1.1.1", ServerName: "cloudflare-dns.com"}},
			Final:    "remote",
			Failover: []string{"cloudflare", "quad9"},
		},
	}
	for name, y := range cases {
		if _, err := buildDNS(y); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestSetFinalProviderRepointsEndpointKeepsTag(t *testing.T) {
	d, err := buildDNS(&dnsYAML{
		Servers: []dnsServerYAML{
			{Name: "remote", Provider: "cloudflare", Method: "tls", Detour: "vpn"},
			{Name: "lan", Type: "local"},
		},
		Final: "remote", Failover: []string{"cloudflare", "quad9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetFinalProvider("quad9"); err != nil {
		t.Fatalf("SetFinalProvider: %v", err)
	}
	s := d.Servers[0]
	if s.Name != "remote" || s.Type != "tls" || s.Detour != "vpn" {
		t.Errorf("tag/transport/detour must survive the re-point: %+v", s)
	}
	if s.Provider != "quad9" || s.Address != "9.9.9.9" || s.ServerName != "dns.quad9.net" {
		t.Errorf("endpoint must move to quad9: %+v", s)
	}
	if err := d.SetFinalProvider("nope"); err == nil {
		t.Error("SetFinalProvider must reject an unknown alias")
	}
}

// LOT-83: the Direct resolver — what a direct/zapret rung resolves through — can
// rotate to a public provider too, so a pinned office DNS that is unreachable on
// another network does not leave the rung unable to resolve at all.
func TestBuildDNSDirectFailoverValidatesAndRepoints(t *testing.T) {
	d, err := buildDNS(&dnsYAML{
		Servers: []dnsServerYAML{
			{Name: "direct-pub", Provider: "cloudflare", Method: "https", Detour: "direct"},
			{Name: "remote", Provider: "quad9", Method: "tls", Detour: "vpn"},
		},
		Direct: "direct-pub", Final: "remote",
		DirectFailover: []string{"cloudflare", "google", "adguard"},
	})
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	if len(d.DirectFailover) != 3 || d.DirectFailover[1] != "google" {
		t.Fatalf("direct_failover not carried: %v", d.DirectFailover)
	}
	if err := d.SetDirectProvider("adguard"); err != nil {
		t.Fatalf("SetDirectProvider: %v", err)
	}
	s := d.Servers[0]
	if s.Name != "direct-pub" || s.Type != "https" || s.Detour != "direct" {
		t.Errorf("tag/transport/detour must survive: %+v", s)
	}
	if s.Provider != "adguard" || s.Address != "94.140.14.14" {
		t.Errorf("endpoint must move to adguard: %+v", s)
	}
}

// A manual Direct (office DNS) is exactly the case LOT-83 fixes: it is accepted,
// and the first rotation REPLACES it with an encrypted provider so the rung can
// still resolve on a network where the office DNS does not answer.
func TestBuildDNSDirectFailoverReplacesManualDirect(t *testing.T) {
	d, err := buildDNS(&dnsYAML{
		Servers: []dnsServerYAML{
			{Name: "office", Type: "udp", Address: "192.168.10.30", Detour: "direct"},
			{Name: "remote", Provider: "cloudflare"},
		},
		Direct: "office", Final: "remote",
		DirectFailover: []string{"cloudflare", "quad9"},
	})
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	if len(d.DirectFailover) != 2 {
		t.Fatalf("direct_failover not carried: %v", d.DirectFailover)
	}
	if err := d.SetDirectProvider("quad9"); err != nil {
		t.Fatalf("SetDirectProvider on a manual Direct: %v", err)
	}
	s := d.Servers[0]
	if s.Type != "https" || s.Provider != "quad9" || s.Address != "9.9.9.9" || s.ServerName != "dns.quad9.net" {
		t.Errorf("manual office DNS must be replaced by an encrypted provider: %+v", s)
	}
	if s.Name != "office" || s.Detour != "direct" {
		t.Errorf("tag/detour must survive: %+v", s)
	}
}

// Explicit URI form: the scheme IS the transport, no catalog alias, no method.
func TestBuildDNSParsesExplicitURLs(t *testing.T) {
	d, err := buildDNS(&dnsYAML{
		Servers: []dnsServerYAML{
			{Name: "doh", URL: "https://cloudflare-dns.com/dns-query", Detour: "direct"},
			{Name: "doq", URL: "quic://dns.quad9.net"},
			{Name: "dot", URL: "tls://1.1.1.1:853"},
			{Name: "plain", URL: "udp://192.168.10.30"},
			{Name: "lan", Type: "local"},
		},
		Direct: "lan", Final: "doh",
	})
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	doh := d.Servers[0]
	if doh.Type != "https" || doh.Address != "cloudflare-dns.com" || doh.Path != "/dns-query" || doh.ServerName != "cloudflare-dns.com" || !doh.Bootstrap {
		t.Errorf("DoH url mis-parsed: %+v", doh)
	}
	doq := d.Servers[1]
	if doq.Type != "quic" || doq.Address != "dns.quad9.net" || doq.ServerName != "dns.quad9.net" || doq.Path != "" {
		t.Errorf("DoQ url mis-parsed: %+v", doq)
	}
	dot := d.Servers[2]
	if dot.Type != "tls" || dot.Address != "1.1.1.1" || dot.Port != 853 || dot.Bootstrap {
		t.Errorf("DoT url mis-parsed (an IP needs no bootstrap): %+v", dot)
	}
	plain := d.Servers[3]
	if plain.Type != "udp" || plain.Address != "192.168.10.30" || plain.Port != 0 {
		t.Errorf("plain url mis-parsed: %+v", plain)
	}
}

func TestBuildDNSRejectsBadURL(t *testing.T) {
	if _, err := buildDNS(&dnsYAML{
		Servers: []dnsServerYAML{{Name: "x", URL: "ftp://nope.example"}},
		Final:   "x",
	}); err == nil {
		t.Error("an unsupported scheme must be rejected")
	}
}

func TestBuildDNSNilOrEmptyIsNoConfig(t *testing.T) {
	if d, err := buildDNS(nil); err != nil || d != nil {
		t.Errorf("nil dns yaml => (nil,nil), got (%v,%v)", d, err)
	}
	if d, err := buildDNS(&dnsYAML{}); err != nil || d != nil {
		t.Errorf("empty dns yaml => (nil,nil), got (%v,%v)", d, err)
	}
}
