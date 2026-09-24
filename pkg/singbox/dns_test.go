package singbox

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
)

func TestDNSServerObjectShapes(t *testing.T) {
	// vpn_url_test is a live outbound; anything else is a dangling detour.
	valid := func(tag string) bool { return tag == "vpn_url_test" }
	cases := []struct {
		name string
		in   DNSServer
		want map[string]any
	}{
		// A "direct" detour is DROPPED, not emitted: sing-box refuses a DNS server
		// detoured to the empty direct outbound. No detour == dial direct.
		{"udp-direct-dropped", DNSServer{Tag: "d", Type: "udp", Server: "192.168.1.1", Detour: "direct"},
			map[string]any{"tag": "d", "type": "udp", "server": "192.168.1.1", "server_port": 53}},
		{"doh-ip-sni", DNSServer{Tag: "r", Type: "https", Server: "1.1.1.1", ServerName: "cloudflare-dns.com", Detour: "vpn_url_test"},
			map[string]any{"tag": "r", "type": "https", "server": "1.1.1.1", "server_port": 443, "path": "/dns-query", "tls": map[string]any{"server_name": "cloudflare-dns.com"}, "detour": "vpn_url_test"}},
		{"dot", DNSServer{Tag: "t", Type: "tls", Server: "9.9.9.9", Detour: "vpn_url_test"},
			map[string]any{"tag": "t", "type": "tls", "server": "9.9.9.9", "server_port": 853, "detour": "vpn_url_test"}},
		{"doq", DNSServer{Tag: "q", Type: "quic", Server: "94.140.14.14", Detour: "vpn_url_test"},
			map[string]any{"tag": "q", "type": "quic", "server": "94.140.14.14", "server_port": 853, "detour": "vpn_url_test"}},
		{"doh-host-bootstrap", DNSServer{Tag: "h", Type: "https", Server: "dns.google", ServerName: "dns.google", Bootstrap: "dns_direct", Detour: "vpn_url_test"},
			map[string]any{"tag": "h", "type": "https", "server": "dns.google", "server_port": 443, "path": "/dns-query", "tls": map[string]any{"server_name": "dns.google"}, "domain_resolver": "dns_direct", "detour": "vpn_url_test"}},
		{"local", DNSServer{Tag: "l", Type: "local"},
			map[string]any{"tag": "l", "type": "local"}},
		{"dangling-detour-dropped", DNSServer{Tag: "x", Type: "udp", Server: "1.1.1.1", Detour: "no-such-pool"},
			map[string]any{"tag": "x", "type": "udp", "server": "1.1.1.1", "server_port": 53}},
	}
	for _, c := range cases {
		if got := dnsServerObject(c.in, valid); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %#v\n want %#v", c.name, got, c.want)
		}
	}
}

func TestDNSSectionSplitAndVersionGate(t *testing.T) {
	o := &DNSOptions{
		Servers: []DNSServer{
			{Tag: "dns_remote", Type: "https", Server: "cloudflare-dns.com", ServerName: "cloudflare-dns.com", Bootstrap: "dns_direct", Detour: "vpn_url_test"},
			{Tag: "dns_direct", Type: "local"},
		},
		Direct: "dns_direct", Final: "dns_remote",
	}
	valid := func(tag string) bool { return tag == "vpn_url_test" }

	// The whole new-DNS format fails to load on <=1.11 — must be gated off.
	if _, ok := dnsSection(o, []string{"ru-direct"}, nil, nil, valid, Capabilities("1.11.0")); ok {
		t.Error("new DNS format must be gated off below 1.12")
	}

	dns, ok := dnsSection(o, []string{"ru-direct"}, nil, nil, valid, Capabilities("1.12.17"))
	if !ok {
		t.Fatal("expected a dns block on 1.12")
	}
	if dns["final"] != "dns_remote" || dns["strategy"] != "prefer_ipv4" || dns["independent_cache"] != true {
		t.Errorf("dns block scalars wrong: %v", dns)
	}
	rules := dns["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("want 1 direct rule, got %d: %v", len(rules), rules)
	}
	r := rules[0].(map[string]any)
	if r["action"] != "route" || r["server"] != "dns_direct" { // explicit action:route (never rely on the implicit default)
		t.Errorf("direct rule must be an explicit route to dns_direct: %v", r)
	}
	if rs, ok := r["rule_set"].([]string); !ok || len(rs) != 1 || rs[0] != "ru-direct" {
		t.Errorf("direct rule must match the ru-direct rule_set: %v", r["rule_set"])
	}
}

// LOT-83: zapret-class services' domains resolve through a separate public
// resolver, emitted AFTER the direct (office) rule and BEFORE the final (VPN) one.
func TestDNSSectionZapretSplit(t *testing.T) {
	o := &DNSOptions{
		Servers: []DNSServer{
			{Tag: "dns_remote", Type: "https", Server: "1.1.1.1", Detour: "vpn_url_test"},
			{Tag: "dns_office", Type: "local"},
			{Tag: "dns_zapret", Type: "https", Server: "9.9.9.9", ServerName: "dns.quad9.net"},
		},
		Direct: "dns_office", Zapret: "dns_zapret", Final: "dns_remote",
	}
	dns, ok := dnsSection(o,
		[]string{"ru-direct"}, []string{"geosite-censor"}, []string{"youtube.com", "x.com"},
		func(string) bool { return true }, Capabilities("1.12.17"))
	if !ok {
		t.Fatal("expected a dns block")
	}
	rules := dns["rules"].([]any)
	// direct, then zapret rule_set, then zapret domain_suffix.
	if len(rules) != 3 {
		t.Fatalf("want 3 rules (direct, zapret rule_set, zapret domain), got %d: %v", len(rules), rules)
	}
	if r := rules[0].(map[string]any); r["server"] != "dns_office" {
		t.Errorf("rule 0 must be the direct/office split: %v", r)
	}
	zrs := rules[1].(map[string]any)
	if zrs["server"] != "dns_zapret" {
		t.Errorf("zapret rule_set must route to dns_zapret: %v", zrs)
	}
	if rs, ok := zrs["rule_set"].([]string); !ok || len(rs) != 1 || rs[0] != "geosite-censor" {
		t.Errorf("zapret rule_set wrong: %v", zrs["rule_set"])
	}
	zd := rules[2].(map[string]any)
	if zd["server"] != "dns_zapret" {
		t.Errorf("zapret domain rule must route to dns_zapret: %v", zd)
	}
	if ds, ok := zd["domain_suffix"].([]string); !ok || len(ds) != 2 {
		t.Errorf("zapret domain_suffix wrong: %v", zd["domain_suffix"])
	}
}

// A "direct" detour on a DNS server is NOT emitted: sing-box refuses to start
// with a DNS server detoured to the empty direct outbound, and omitting the detour
// IS "dial direct". Regression for the LOT-83 zapret-dns server that killed sing-box.
func TestDNSServerDropsDirectDetour(t *testing.T) {
	o := &DNSOptions{
		Servers: []DNSServer{
			{Tag: "zapret-dns", Type: "tls", Server: "9.9.9.9", ServerName: "dns.quad9.net", Detour: "direct"},
			{Tag: "dns_remote", Type: "https", Server: "1.1.1.1", Detour: "vpn_url_test"},
		},
		Final: "dns_remote",
	}
	dns, ok := dnsSection(o, nil, nil, nil, func(tag string) bool { return tag == "vpn_url_test" }, Capabilities("1.12.17"))
	if !ok {
		t.Fatal("expected a dns block")
	}
	servers := dns["servers"].([]any)
	zapret := servers[0].(map[string]any)
	if _, present := zapret["detour"]; present {
		t.Errorf("a direct detour must be dropped, not emitted: %v", zapret)
	}
	remote := servers[1].(map[string]any)
	if remote["detour"] != "vpn_url_test" {
		t.Errorf("a real outbound detour must survive: %v", remote)
	}
}

func TestDNSSectionFakeIP(t *testing.T) {
	o := &DNSOptions{
		Servers: []DNSServer{{Tag: "dns_remote", Type: "https", Server: "1.1.1.1", Detour: "vpn_url_test"}},
		Final:   "dns_remote",
		FakeIP:  &FakeIPOptions{},
	}
	dns, ok := dnsSection(o, nil, nil, nil, func(string) bool { return true }, Capabilities("1.12.17"))
	if !ok {
		t.Fatal("expected a dns block")
	}
	servers := dns["servers"].([]any)
	last := servers[len(servers)-1].(map[string]any) // fakeip is appended after the real servers
	if last["type"] != "fakeip" || last["inet4_range"] != "198.18.0.0/15" || last["inet6_range"] != "fc00::/18" {
		t.Errorf("fakeip server object wrong: %v", last)
	}
	rules := dns["rules"].([]any)
	fr := rules[len(rules)-1].(map[string]any) // A/AAAA->fakeip rule is last (after any bypass)
	if fr["server"] != "dns_fakeip" || fr["action"] != "route" {
		t.Errorf("fakeip A/AAAA rule wrong: %v", fr)
	}
	if qt, ok := fr["query_type"].([]string); !ok || len(qt) != 2 {
		t.Errorf("fakeip rule must match A/AAAA: %v", fr["query_type"])
	}
}

func TestGenerateEmitsSplitDNSWithSurvivingDetour(t *testing.T) {
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?type=ws#v", subscription.FormatSingleURL)
	services := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}
	memberships := map[string][]subscription.Node{"vpn_url_test": {vless}}

	opts := DefaultOptions()
	opts.DNS = &DNSOptions{
		Servers: []DNSServer{
			{Tag: "dns_remote", Type: "https", Server: "cloudflare-dns.com", ServerName: "cloudflare-dns.com", Bootstrap: "dns_direct", Detour: "vpn_url_test"},
			{Tag: "dns_direct", Type: "local"},
		},
		Direct: "dns_direct", Final: "dns_remote",
	}
	res, err := Generate(services, nil, []subscription.Node{vless}, memberships, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(res.JSON, &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		t.Fatal("no dns block emitted")
	}
	if dns["final"] != "dns_remote" {
		t.Errorf("final = %v, want dns_remote", dns["final"])
	}
	servers := dns["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("want 2 dns servers, got %d", len(servers))
	}
	// vpn_url_test is a live outbound here, so the DoH server's detour survives.
	if remote := servers[0].(map[string]any); remote["detour"] != "vpn_url_test" {
		t.Errorf("remote resolver detour should survive when the pool exists: %v", remote)
	}
	// sing-box 1.12 requires route.default_domain_resolver whenever a dns block is
	// present, else `sing-box check` FATALs on outbounds that dial a hostname.
	route := cfg["route"].(map[string]any)
	ddr, ok := route["default_domain_resolver"].(map[string]any)
	if !ok || ddr["server"] != "dns_direct" {
		t.Errorf("route.default_domain_resolver must point at the direct resolver: %v", route["default_domain_resolver"])
	}
}
