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
		"unknown provider": {Servers: []dnsServerYAML{{Name: "x", Provider: "nope"}}, Final: "x"},
		"final not declared": {Servers: []dnsServerYAML{{Name: "a", Type: "local"}}, Final: "b"},
		"bad strategy":       {Servers: []dnsServerYAML{{Name: "a", Type: "local"}}, Final: "a", Strategy: "fastest"},
		"duplicate name":     {Servers: []dnsServerYAML{{Name: "a", Type: "local"}, {Name: "a", Type: "local"}}, Final: "a"},
		"no type no provider": {Servers: []dnsServerYAML{{Name: "a"}}, Final: "a"},
		"direct not declared": {Servers: []dnsServerYAML{{Name: "a", Type: "local"}}, Final: "a", Direct: "z"},
	}
	for name, y := range cases {
		if _, err := buildDNS(y); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
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
