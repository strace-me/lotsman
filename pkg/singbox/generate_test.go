package singbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/subscription"
)

func mustParse(t *testing.T, raw string, f subscription.Format) subscription.Node {
	t.Helper()
	ns, err := subscription.Parse([]byte(raw), f, "test")
	if err != nil || len(ns) != 1 {
		t.Fatalf("parse %q: err=%v n=%d", raw, err, len(ns))
	}
	return ns[0]
}

func svc(name, pool string, ruleSets ...string) registry.Service {
	return registry.Service{
		Name:     name,
		RuleSets: ruleSets,
		Chain: []registry.ChainStep{
			{Position: 0, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: pool},
		},
	}
}

func outboundsByTag(t *testing.T, js []byte) (map[string]map[string]any, map[string][]map[string]any, map[string]any) {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(js, &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	byTag := map[string]map[string]any{}
	byType := map[string][]map[string]any{}
	for _, o := range cfg["outbounds"].([]any) {
		m := o.(map[string]any)
		if tag, ok := m["tag"].(string); ok {
			byTag[tag] = m
		}
		byType[m["type"].(string)] = append(byType[m["type"].(string)], m)
	}
	return byTag, byType, cfg
}

func TestGeneratePerServiceSelectors(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://secretpass@45.91.54.162:443?sni=magic.example", subscription.FormatSingleURL)
	native := mustParse(t, `{"outbounds":[{"type":"hysteria2","tag":"nat","server":"5.6.7.8","server_port":443,"password":"pw2","tls":{"enabled":true,"server_name":"x.example"}}]}`, subscription.FormatSingbox)
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?type=ws#v", subscription.FormatSingleURL)

	services := []registry.Service{
		svc("youtube", "vpn_url_test", "geosite-youtube"),
		svc("discord", "vpn_url_test", "geosite-discord", "geoip-telegram"),
	}
	nodes := []subscription.Node{hy2, native, vless}
	memberships := map[string][]subscription.Node{"vpn_url_test": {hy2, native, vless}}

	res, err := Generate(services, nil, nodes, memberships, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped = %v, want none (all protocols supported)", res.Skipped)
	}

	byTag, byType, cfg := outboundsByTag(t, res.JSON)

	// clash-api + tproxy.
	if cfg["experimental"].(map[string]any)["clash_api"].(map[string]any)["external_controller"] != "127.0.0.1:9090" {
		t.Error("clash_api controller missing")
	}
	if cfg["inbounds"].([]any)[0].(map[string]any)["listen_port"].(float64) != 7893 {
		t.Error("tproxy port wrong")
	}

	// Two hy2 outbounds (url + native passthrough) and the vless node.
	if len(byType["hysteria2"]) != 2 || len(byType["vless"]) != 1 {
		t.Errorf("outbounds hy2=%d vless=%d", len(byType["hysteria2"]), len(byType["vless"]))
	}

	// One selector per service, each defaulting to its VPN pool.
	for _, name := range []string{"youtube", "discord"} {
		sel := byTag[registry.SelectorTag(name)]
		if sel == nil || sel["type"] != "selector" {
			t.Fatalf("selector for %s missing: %v", name, sel)
		}
		if sel["default"] != "vpn_url_test" {
			t.Errorf("%s selector default = %v, want vpn_url_test", name, sel["default"])
		}
		// The selector must also list the pool's CONCRETE node tags, so
		// pkg/noderank can pin one with a valid Clash PUT. Every concrete node
		// outbound (hysteria2/vless, not the url-test group) must be a member.
		members := map[string]bool{}
		for _, o := range sel["outbounds"].([]any) {
			members[o.(string)] = true
		}
		for _, ct := range []string{"vpn_url_test", "direct"} {
			if !members[ct] {
				t.Errorf("%s selector missing %q member", name, ct)
			}
		}
		var concrete int
		for tag, ob := range byTag {
			switch ob["type"] {
			case "hysteria2", "vless":
				if members[tag] {
					concrete++
				}
			}
		}
		if concrete == 0 {
			t.Errorf("%s selector lists no concrete node (noderank cannot pin)", name)
		}
	}

	// Route rules map each service's rule-sets to its selector.
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	foundYT, foundDiscord := false, false
	for _, r := range rules {
		m := r.(map[string]any)
		if m["outbound"] == registry.SelectorTag("youtube") {
			foundYT = true
		}
		if m["outbound"] == registry.SelectorTag("discord") {
			foundDiscord = true
		}
	}
	if !foundYT || !foundDiscord {
		t.Errorf("route rules missing per-service selector routing yt=%v discord=%v", foundYT, foundDiscord)
	}

	// All referenced rule-sets are defined.
	defined := map[string]bool{}
	for _, d := range cfg["route"].(map[string]any)["rule_set"].([]any) {
		defined[d.(map[string]any)["tag"].(string)] = true
	}
	for _, rs := range []string{"geosite-youtube", "geosite-discord", "geoip-telegram"} {
		if !defined[rs] {
			t.Errorf("rule-set %q referenced but not defined", rs)
		}
	}
}

func TestGenerateDomainAndIPRoutes(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://pw@1.2.3.4:443?sni=x.example", subscription.FormatSingleURL)
	discord := svc("discord", "vpn_url_test")
	discord.Domains = []string{"discord.media", "*.discord.gg"}
	discord.IPs = []string{"66.22.196.0/22"} // voice (geoip replacement)

	opts := DefaultOptions()
	opts.SocksProbeListen = "127.0.0.1:7891"
	res, err := Generate([]registry.Service{discord}, nil, []subscription.Node{hy2},
		map[string][]subscription.Node{"vpn_url_test": {hy2}}, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, _, cfg := outboundsByTag(t, res.JSON)
	sel := registry.SelectorTag("discord")

	// socks probe-in inbound present.
	var hasSocks bool
	for _, in := range cfg["inbounds"].([]any) {
		if m := in.(map[string]any); m["type"] == "socks" && m["tag"] == "probe-in" {
			hasSocks = true
			if m["listen_port"].(float64) != 7891 {
				t.Errorf("probe-in port = %v, want 7891", m["listen_port"])
			}
		}
	}
	if !hasSocks {
		t.Error("socks probe-in inbound missing")
	}

	// A domain_suffix rule and an ip_cidr rule, both -> discord's selector.
	var domainRule, ipRule bool
	for _, r := range cfg["route"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		if m["outbound"] != sel {
			continue
		}
		if _, ok := m["domain_suffix"]; ok {
			domainRule = true
		}
		if _, ok := m["ip_cidr"]; ok {
			ipRule = true
		}
	}
	if !domainRule || !ipRule {
		t.Errorf("missing route rules for discord: domain=%v ip=%v", domainRule, ipRule)
	}
}

func TestPerSourceDeviceRouting(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)
	services := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}
	devices := []registry.Device{
		{Name: "gaming-pc", Sources: []string{"192.168.1.50/32"}, Policy: "direct"},
		{Name: "kid-tablet", Sources: []string{"192.168.1.60/32"}, Policy: "vpn_url_test"},
		{Name: "iot-cam", Sources: []string{"192.168.1.70/32"}, Policy: "block"},
	}
	memberships := map[string][]subscription.Node{"vpn_url_test": {hy2}}

	res, err := Generate(services, devices, []subscription.Node{hy2}, memberships, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var cfg map[string]any
	json.Unmarshal(res.JSON, &cfg)
	rules := cfg["route"].(map[string]any)["rules"].([]any)

	// Find each device's source rule and check policy + ordering (device rules
	// must precede the service rule-set rules).
	var gamingIdx, serviceIdx = -1, -1
	for i, r := range rules {
		m := r.(map[string]any)
		src, _ := m["source_ip_cidr"].([]any)
		if len(src) == 1 && src[0] == "192.168.1.50/32" {
			if m["outbound"] != "direct" {
				t.Errorf("gaming-pc -> %v, want direct", m["outbound"])
			}
			gamingIdx = i
		}
		if len(src) == 1 && src[0] == "192.168.1.60/32" && m["outbound"] != "vpn_url_test" {
			t.Errorf("kid-tablet -> %v, want vpn_url_test", m["outbound"])
		}
		if len(src) == 1 && src[0] == "192.168.1.70/32" && m["action"] != "reject" {
			t.Errorf("iot-cam -> %v, want reject (block)", m)
		}
		if m["outbound"] == registry.SelectorTag("youtube") {
			serviceIdx = i
		}
	}
	if gamingIdx == -1 || serviceIdx == -1 || gamingIdx > serviceIdx {
		t.Errorf("device rules must come before service rules (gaming=%d service=%d)", gamingIdx, serviceIdx)
	}
}

func TestDirectOnlyServiceAndPriorityOrder(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)

	ruDirect := registry.Service{
		Name: "ru-direct", RuleSets: []string{"geosite-ru"}, Priority: -10,
		Chain: []registry.ChainStep{{State: registry.StateLocked, StrategyClass: strategy.ClassDirect, StrategyID: "direct"}},
	}
	yt := svc("youtube", "vpn_url_test", "geosite-youtube") // Priority 0
	blocked := svc("web-blocked", "vpn_url_test", "geosite-ru-blocked")
	blocked.Priority = 10
	// Pass unsorted to prove the generator orders by Priority, not input order.
	services := []registry.Service{blocked, yt, ruDirect}
	memberships := map[string][]subscription.Node{"vpn_url_test": {hy2}}

	res, err := Generate(services, nil, []subscription.Node{hy2}, memberships, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	byTag, _, cfg := outboundsByTag(t, res.JSON)

	// A direct/LOCKED service must NOT get a flippable selector.
	if byTag[registry.SelectorTag("ru-direct")] != nil {
		t.Error("ru-direct (LOCKED) must route to direct, not a selector")
	}

	rules := cfg["route"].(map[string]any)["rules"].([]any)
	ruIdx, ytIdx, blockedIdx := -1, -1, -1
	for i, r := range rules {
		m := r.(map[string]any)
		out, _ := m["outbound"].(string)
		if out == "direct" {
			if rs, ok := m["rule_set"].([]any); ok {
				for _, tg := range rs {
					if tg == "geosite-ru" {
						ruIdx = i
					}
				}
			}
		}
		if out == registry.SelectorTag("youtube") && ytIdx == -1 {
			ytIdx = i
		}
		if out == registry.SelectorTag("web-blocked") && blockedIdx == -1 {
			blockedIdx = i
		}
	}
	if ruIdx == -1 || ytIdx == -1 || blockedIdx == -1 {
		t.Fatalf("missing rules: ru=%d yt=%d blocked=%d", ruIdx, ytIdx, blockedIdx)
	}
	// geosite-ru -> direct must be FIRST; the catch-all web-blocked LAST.
	if !(ruIdx < ytIdx && ytIdx < blockedIdx) {
		t.Errorf("rule order ru=%d yt=%d blocked=%d, want ru < yt < blocked", ruIdx, ytIdx, blockedIdx)
	}
}

// Domain rules must precede IP rules globally, even when the IP-matching service
// has a more-first Priority — so SNI/domain disambiguates a shared IP before any
// broad ip_cidr can hijack it.
func TestDomainRulesBeatIPRules(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)

	ipSvc := svc("voice", "vpn_url_test") // ip_cidr only, tries to jump ahead
	ipSvc.IPs = []string{"66.22.192.0/18"}
	ipSvc.Priority = -5 // more-first than the domain service
	domSvc := svc("youtube", "vpn_url_test", "geosite-youtube")
	domSvc.Priority = 0

	res, err := Generate([]registry.Service{ipSvc, domSvc}, nil,
		[]subscription.Node{hy2}, map[string][]subscription.Node{"vpn_url_test": {hy2}}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var cfg map[string]any
	json.Unmarshal(res.JSON, &cfg)
	rules := cfg["route"].(map[string]any)["rules"].([]any)

	lastDomainIdx, firstIPIdx := -1, -1
	for i, r := range rules {
		m := r.(map[string]any)
		if _, ok := m["rule_set"]; ok {
			lastDomainIdx = i
		}
		if _, ok := m["domain_suffix"]; ok {
			lastDomainIdx = i
		}
		if _, ok := m["ip_cidr"]; ok && firstIPIdx == -1 {
			firstIPIdx = i
		}
	}
	if lastDomainIdx == -1 || firstIPIdx == -1 {
		t.Fatalf("missing tiers: lastDomain=%d firstIP=%d", lastDomainIdx, firstIPIdx)
	}
	if firstIPIdx < lastDomainIdx {
		t.Errorf("ip_cidr rule at %d precedes a domain rule at %d; domain tier must come first", firstIPIdx, lastDomainIdx)
	}
}

func TestTLSFragmentKnob(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)
	frag := svc("youtube", "vpn_url_test", "geosite-youtube")
	frag.TLSFragment = true
	plain := svc("discord", "vpn_url_test", "geosite-discord") // no fragment

	res, err := Generate([]registry.Service{frag, plain}, nil,
		[]subscription.Node{hy2}, map[string][]subscription.Node{"vpn_url_test": {hy2}}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var cfg map[string]any
	json.Unmarshal(res.JSON, &cfg)
	rules := cfg["route"].(map[string]any)["rules"].([]any)

	var fragRule, plainRule map[string]any
	for _, r := range rules {
		m := r.(map[string]any)
		if m["outbound"] == registry.SelectorTag("youtube") {
			fragRule = m
		}
		if m["outbound"] == registry.SelectorTag("discord") {
			plainRule = m
		}
	}
	if fragRule == nil || plainRule == nil {
		t.Fatalf("rules missing: frag=%v plain=%v", fragRule, plainRule)
	}
	if fragRule["tls_fragment"] != true || fragRule["tls_record_fragment"] != true || fragRule["action"] != "route" {
		t.Errorf("fragment service rule = %v, want action=route + tls_fragment + tls_record_fragment", fragRule)
	}
	if _, has := plainRule["tls_fragment"]; has {
		t.Errorf("non-fragment service must not carry tls_fragment, got %v", plainRule)
	}
}

func TestAnyTLSOutbound(t *testing.T) {
	n := mustParse(t, "anytls://pw123@192.0.2.11:8443?sni=www.bing.com&insecure=1#DE-anytls", subscription.FormatSingleURL)
	if n.Protocol != "anytls" {
		t.Fatalf("protocol = %q, want anytls", n.Protocol)
	}
	res, err := Generate(nil, nil, []subscription.Node{n}, map[string][]subscription.Node{}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)
	if len(byType["anytls"]) != 1 {
		t.Fatalf("anytls outbound not emitted (skipped=%v)", res.Skipped)
	}
	ob := byType["anytls"][0]
	if ob["password"] != "pw123" || ob["server"] != "192.0.2.11" {
		t.Errorf("anytls outbound = %v", ob)
	}
	tls := ob["tls"].(map[string]any)
	if tls["server_name"] != "www.bing.com" || tls["insecure"] != true {
		t.Errorf("anytls tls = %v, want sni www.bing.com + insecure", tls)
	}
}

func TestTUICOutbound(t *testing.T) {
	link := "tuic://72df3c80-b7f8-c614-20bd-9b78b1b34f70:secretpw@192.0.2.11:2097/?congestion_control=bbr&alpn=h3&sni=www.bing.com&allow_insecure=1&udp_relay_mode=native#DE-tuic"
	n := mustParse(t, link, subscription.FormatSingleURL)
	if n.Protocol != "tuic" || !n.Caps.UDPNative {
		t.Fatalf("tuic node = proto %q udp %v", n.Protocol, n.Caps.UDPNative)
	}
	res, err := Generate(nil, nil, []subscription.Node{n}, map[string][]subscription.Node{}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)
	if len(byType["tuic"]) != 1 {
		t.Fatalf("tuic outbound not emitted (skipped=%v)", res.Skipped)
	}
	ob := byType["tuic"][0]
	if ob["uuid"] != "72df3c80-b7f8-c614-20bd-9b78b1b34f70" || ob["password"] != "secretpw" {
		t.Errorf("tuic creds = uuid %v pw %v", ob["uuid"], ob["password"])
	}
	if ob["congestion_control"] != "bbr" || ob["udp_relay_mode"] != "native" {
		t.Errorf("tuic opts = %v / %v", ob["congestion_control"], ob["udp_relay_mode"])
	}
	tls := ob["tls"].(map[string]any)
	if tls["server_name"] != "www.bing.com" || tls["insecure"] != true {
		t.Errorf("tuic tls = %v", tls)
	}
}

func TestFakeIPKnob(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@1.2.3.4:443?sni=x", subscription.FormatSingleURL)
	svcs := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}
	mem := map[string][]subscription.Node{"vpn_url_test": {hy2}}

	// Off: no dns block (box's own resolver stays in charge).
	res, _ := Generate(svcs, nil, []subscription.Node{hy2}, mem, DefaultOptions())
	var off map[string]any
	json.Unmarshal(res.JSON, &off)
	if _, has := off["dns"]; has {
		t.Error("fakeip off: must not emit a dns block")
	}

	// On: dns block with fakeip server + resolver + A/AAAA rule.
	opts := DefaultOptions()
	opts.FakeIP = &FakeIPOptions{Resolver: "https://1.1.1.1/dns-query"}
	res, _ = Generate(svcs, nil, []subscription.Node{hy2}, mem, opts)
	var on map[string]any
	json.Unmarshal(res.JSON, &on)
	dns, ok := on["dns"].(map[string]any)
	if !ok {
		t.Fatal("fakeip on: dns block missing")
	}
	servers := dns["servers"].([]any)
	if len(servers) != 2 || servers[0].(map[string]any)["type"] != "fakeip" {
		t.Errorf("dns servers = %v, want [fakeip, resolver]", servers)
	}
	if servers[0].(map[string]any)["inet4_range"] != "198.18.0.0/15" {
		t.Errorf("default inet4_range missing: %v", servers[0])
	}
	if r := servers[1].(map[string]any); r["type"] != "https" || r["server"] != "1.1.1.1" {
		t.Errorf("resolver = %v, want https 1.1.1.1", r)
	}
	rule := dns["rules"].([]any)[0].(map[string]any)
	if rule["server"] != "fakeip" {
		t.Errorf("dns rule must route to fakeip, got %v", rule)
	}
}

func TestHy2PortHoppingAndBrutal(t *testing.T) {
	// Node declaring port-hopping (mport range) + brutal bandwidth.
	hop := mustParse(t, "hysteria2://pw@1.2.3.4:443?sni=x&mport=20000-30000&hop_interval=20s&upmbps=50&downmbps=100", subscription.FormatSingleURL)
	// Plain node (like fastvpn) — must be unaffected.
	plain := mustParse(t, "hysteria2://pw@5.6.7.8:443?sni=y", subscription.FormatSingleURL)

	res, err := Generate(nil, nil, []subscription.Node{hop, plain},
		map[string][]subscription.Node{}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)
	var hopOb, plainOb map[string]any
	for _, o := range byType["hysteria2"] {
		if o["server"] == "1.2.3.4" {
			hopOb = o
		}
		if o["server"] == "5.6.7.8" {
			plainOb = o
		}
	}
	if hopOb == nil || plainOb == nil {
		t.Fatalf("outbounds missing")
	}
	ports, _ := hopOb["server_ports"].([]any)
	if len(ports) != 1 || ports[0] != "20000:30000" {
		t.Errorf("server_ports = %v, want [20000:30000]", hopOb["server_ports"])
	}
	if hopOb["hop_interval"] != "20s" {
		t.Errorf("hop_interval = %v, want 20s", hopOb["hop_interval"])
	}
	if toNum(hopOb["up_mbps"]) != 50 || toNum(hopOb["down_mbps"]) != 100 {
		t.Errorf("brutal = up %v down %v, want 50/100", hopOb["up_mbps"], hopOb["down_mbps"])
	}
	// Plain node must carry none of these.
	for _, k := range []string{"server_ports", "hop_interval", "up_mbps", "down_mbps"} {
		if _, has := plainOb[k]; has {
			t.Errorf("plain node must not have %q (opt-in only)", k)
		}
	}
}

func toNum(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return -1
}

func TestVersionGateDropsUnsupportedKnobs(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)
	frag := svc("youtube", "vpn_url_test", "geosite-youtube")
	frag.TLSFragment = true

	opts := DefaultOptions()
	opts.TargetVersion = "1.11.0" // before tls_fragment (1.12.0) and utls is fine
	opts.UTLSFingerprint = "chrome"

	res, err := Generate([]registry.Service{frag}, nil,
		[]subscription.Node{hy2}, map[string][]subscription.Node{"vpn_url_test": {hy2}}, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// tls_fragment must be dropped and reported.
	var cfg map[string]any
	json.Unmarshal(res.JSON, &cfg)
	for _, r := range cfg["route"].(map[string]any)["rules"].([]any) {
		if _, has := r.(map[string]any)["tls_fragment"]; has {
			t.Error("tls_fragment must not be emitted for target 1.11.0")
		}
	}
	var reported bool
	for _, k := range res.SkippedKnobs {
		if len(k) >= 12 && k[:12] == "tls_fragment" {
			reported = true
		}
	}
	if !reported {
		t.Errorf("dropped tls_fragment must be reported in SkippedKnobs, got %v", res.SkippedKnobs)
	}
}

func TestUTLSInjection(t *testing.T) {
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?security=tls&sni=ex.com#v", subscription.FormatSingleURL)
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)
	opts := DefaultOptions()
	opts.UTLSFingerprint = "chrome"

	res, err := Generate([]registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}, nil,
		[]subscription.Node{vless, hy2}, map[string][]subscription.Node{"vpn_url_test": {vless, hy2}}, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	byTag, byType, _ := outboundsByTag(t, res.JSON)
	_ = byTag

	// vless (TCP TLS) gets the default fingerprint.
	v := byType["vless"][0]
	utls, ok := v["tls"].(map[string]any)["utls"].(map[string]any)
	if !ok || utls["fingerprint"] != "chrome" {
		t.Errorf("vless utls = %v, want fingerprint chrome", v["tls"])
	}
	// hysteria2 (QUIC) must NOT get utls.
	h := byType["hysteria2"][0]
	if _, has := h["tls"].(map[string]any)["utls"]; has {
		t.Errorf("hysteria2 (QUIC) must not get utls, got %v", h["tls"])
	}
}

func TestMultiplexInjection(t *testing.T) {
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?security=tls&sni=ex.com#v", subscription.FormatSingleURL)
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)
	opts := DefaultOptions()
	opts.Multiplex = &MultiplexOptions{Protocol: "smux", MaxConnections: 4, Padding: true, BrutalUp: 100, BrutalDown: 100}

	res, err := Generate([]registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}, nil,
		[]subscription.Node{vless, hy2}, map[string][]subscription.Node{"vpn_url_test": {vless, hy2}}, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)

	// vless (TCP) gets the default multiplex block with padding + brutal.
	v := byType["vless"][0]
	mux, ok := v["multiplex"].(map[string]any)
	if !ok || mux["protocol"] != "smux" || mux["padding"] != true {
		t.Errorf("vless multiplex = %v, want smux+padding", v["multiplex"])
	}
	if br, ok := mux["brutal"].(map[string]any); !ok || br["up_mbps"] != float64(100) {
		t.Errorf("vless multiplex brutal = %v, want up_mbps 100", mux["brutal"])
	}
	// hysteria2 (QUIC) must NOT get multiplex.
	h := byType["hysteria2"][0]
	if _, has := h["multiplex"]; has {
		t.Errorf("hysteria2 (QUIC) must not get multiplex, got %v", h["multiplex"])
	}
}

func TestMultiplexVersionGated(t *testing.T) {
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?security=tls&sni=ex.com#v", subscription.FormatSingleURL)
	opts := DefaultOptions()
	opts.TargetVersion = "1.0.0" // below the multiplex floor
	opts.Multiplex = &MultiplexOptions{Protocol: "smux"}

	res, err := Generate([]registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}, nil,
		[]subscription.Node{vless}, map[string][]subscription.Node{"vpn_url_test": {vless}}, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)
	if _, has := byType["vless"][0]["multiplex"]; has {
		t.Error("multiplex must be skipped on a sing-box version below the floor")
	}
	if len(res.SkippedKnobs) == 0 {
		t.Error("a gated-out multiplex must be reported in SkippedKnobs")
	}
}

func TestShadowTLSPair(t *testing.T) {
	creds := base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:sspass"))
	raw := "shadowtls://" + creds + "@1.2.3.4:443?version=3&password=STPW&sni=www.bing.com&fp=chrome#st"
	node := mustParse(t, raw, subscription.FormatSingleURL)
	if node.Protocol != subscription.ProtoShadowTLS {
		t.Fatalf("protocol = %q, want shadowtls", node.Protocol)
	}

	res, err := Generate([]registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}, nil,
		[]subscription.Node{node}, map[string][]subscription.Node{"vpn_url_test": {node}}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)

	// primary shadowsocks carries the cipher and detours through shadowtls.
	if len(byType["shadowsocks"]) != 1 || len(byType["shadowtls"]) != 1 {
		t.Fatalf("want 1 shadowsocks + 1 shadowtls, got ss=%d stls=%d", len(byType["shadowsocks"]), len(byType["shadowtls"]))
	}
	ss := byType["shadowsocks"][0]
	st := byType["shadowtls"][0]
	if ss["method"] != "aes-128-gcm" || ss["password"] != "sspass" {
		t.Errorf("ss creds = %v/%v, want aes-128-gcm/sspass", ss["method"], ss["password"])
	}
	if ss["detour"] != st["tag"] {
		t.Errorf("ss detour %v must point at shadowtls tag %v", ss["detour"], st["tag"])
	}
	if st["version"] != float64(3) || st["password"] != "STPW" {
		t.Errorf("shadowtls version/password = %v/%v, want 3/STPW", st["version"], st["password"])
	}
	tls := st["tls"].(map[string]any)
	if tls["server_name"] != "www.bing.com" {
		t.Errorf("shadowtls decoy sni = %v, want www.bing.com", tls["server_name"])
	}
	if u, ok := tls["utls"].(map[string]any); !ok || u["fingerprint"] != "chrome" {
		t.Errorf("shadowtls utls = %v, want chrome", tls["utls"])
	}
}

func TestWireGuardEndpoint(t *testing.T) {
	raw := "wireguard://cHJpdmtleQ@1.2.3.4:51820?peer=cHVia2V5&address=10.0.0.2/32&mtu=1408&reserved=1,2,3#wg"
	node := mustParse(t, raw, subscription.FormatSingleURL)
	if node.Protocol != subscription.ProtoWireGuard {
		t.Fatalf("protocol = %q, want wireguard", node.Protocol)
	}

	res, err := Generate([]registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}, nil,
		[]subscription.Node{node}, map[string][]subscription.Node{"vpn_url_test": {node}}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(res.JSON, &cfg); err != nil {
		t.Fatal(err)
	}
	// WireGuard goes into endpoints, never outbounds.
	eps, ok := cfg["endpoints"].([]any)
	if !ok || len(eps) != 1 {
		t.Fatalf("want 1 endpoint, got %v", cfg["endpoints"])
	}
	ep := eps[0].(map[string]any)
	if ep["type"] != "wireguard" || ep["private_key"] != "cHJpdmtleQ" {
		t.Errorf("endpoint = %v", ep)
	}
	peer := ep["peers"].([]any)[0].(map[string]any)
	if peer["public_key"] != "cHVia2V5" || peer["address"] != "1.2.3.4" {
		t.Errorf("peer = %v", peer)
	}
	for _, o := range cfg["outbounds"].([]any) {
		if o.(map[string]any)["type"] == "wireguard" {
			t.Error("wireguard must be an endpoint, not an outbound")
		}
	}
	if tag, ok := NodeTag(node); !ok || tag == "" {
		t.Errorf("NodeTag(wireguard) = %q,%v want a pinnable tag", tag, ok)
	}
}

func TestECHKnob(t *testing.T) {
	// ech=1 -> enabled (DNS-resolved config at runtime).
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?security=tls&sni=ex.com&ech=1#v", subscription.FormatSingleURL)
	svcs := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}
	mem := map[string][]subscription.Node{"vpn_url_test": {vless}}

	res, err := Generate(svcs, nil, []subscription.Node{vless}, mem, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType, _ := outboundsByTag(t, res.JSON)
	ech, ok := byType["vless"][0]["tls"].(map[string]any)["ech"].(map[string]any)
	if !ok || ech["enabled"] != true {
		t.Errorf("vless ech = %v, want enabled", byType["vless"][0]["tls"])
	}

	// On a target without ECH, the block is stripped + reported.
	opts := DefaultOptions()
	opts.TargetVersion = "1.5.0"
	res2, err := Generate(svcs, nil, []subscription.Node{vless}, mem, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, byType2, _ := outboundsByTag(t, res2.JSON)
	if _, has := byType2["vless"][0]["tls"].(map[string]any)["ech"]; has {
		t.Error("ech must be stripped on a sing-box version below the ECH floor")
	}
	if len(res2.SkippedKnobs) == 0 {
		t.Error("a stripped ech must be reported in SkippedKnobs")
	}
}

func TestWarmupPoolNeverSleeps(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://p@45.91.54.162:443?sni=x", subscription.FormatSingleURL)
	vless := mustParse(t, "vless://uuid@1.2.3.4:443?type=ws#v", subscription.FormatSingleURL)
	services := []registry.Service{svc("youtube", "warm", "geosite-youtube")}
	memberships := map[string][]subscription.Node{"warm": {hy2}, "lazy": {vless}}

	// warm: marked warmup (no explicit interval) -> 1m + idle 0s. lazy: defaults.
	set := &pools.Set{Pools: map[string]pools.Pool{
		"warm": {Name: "warm", Type: pools.TypeURLTest, Warmup: true},
		"lazy": {Name: "lazy", Type: pools.TypeURLTest},
	}}
	opts := DefaultOptions()
	opts.PoolOpts = PoolOptionsFrom(set)

	res, err := Generate(services, nil, []subscription.Node{hy2, vless}, memberships, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	byTag, _, _ := outboundsByTag(t, res.JSON)

	warm := byTag["warm"]
	if warm["idle_timeout"] != "0s" {
		t.Errorf("warmup pool idle_timeout = %v, want 0s (never sleep)", warm["idle_timeout"])
	}
	if warm["interval"] != "1m" {
		t.Errorf("warmup pool interval = %v, want 1m", warm["interval"])
	}
	lazy := byTag["lazy"]
	if _, ok := lazy["idle_timeout"]; ok {
		t.Errorf("non-warmup pool must not set idle_timeout, got %v", lazy["idle_timeout"])
	}
	if lazy["interval"] != "5m" {
		t.Errorf("non-warmup pool interval = %v, want default 5m", lazy["interval"])
	}
}

func TestGenerateEmptyPoolFailsSafeToDirect(t *testing.T) {
	// No nodes -> the service's VPN pool is empty -> selector defaults to direct.
	services := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}
	res, err := Generate(services, nil, nil, map[string][]subscription.Node{"vpn_url_test": nil}, DefaultOptions())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	byTag, _, _ := outboundsByTag(t, res.JSON)
	sel := byTag[registry.SelectorTag("youtube")]
	if sel["default"] != "direct" {
		t.Errorf("empty pool should default selector to direct, got %v", sel["default"])
	}
}

// subViaRule finds the route rule that sends the given host's domain_suffix to an
// outbound, returning that outbound and whether the rule was found.
func subViaRule(t *testing.T, js []byte, host string) (string, bool) {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(js, &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, r := range cfg["route"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		ds, ok := m["domain_suffix"].([]any)
		if !ok {
			continue
		}
		for _, h := range ds {
			if h == host {
				out, _ := m["outbound"].(string)
				return out, true
			}
		}
	}
	return "", false
}

func TestSubscriptionViaPool(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://pw@1.2.3.4:443?sni=x.example", subscription.FormatSingleURL)
	services := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}
	memberships := map[string][]subscription.Node{"vpn_url_test": {hy2}}

	// With a non-empty via-pool and hosts, the endpoint host routes via the pool.
	opts := DefaultOptions()
	opts.SubViaPool = "vpn_url_test"
	opts.SubViaHosts = []string{"panel.example", "panel.example"} // dup tolerated
	res, err := Generate(services, nil, []subscription.Node{hy2}, memberships, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out, ok := subViaRule(t, res.JSON, "panel.example"); !ok || out != "vpn_url_test" {
		t.Errorf("sub host should route via vpn_url_test, got out=%q ok=%v", out, ok)
	}
}

func TestSubscriptionViaPoolFailsafe(t *testing.T) {
	hy2 := mustParse(t, "hysteria2://pw@1.2.3.4:443?sni=x.example", subscription.FormatSingleURL)
	services := []registry.Service{svc("youtube", "vpn_url_test", "geosite-youtube")}

	// Empty pool (no members) -> NO rule (fail-safe to direct fetch).
	opts := DefaultOptions()
	opts.SubViaPool = "vpn_url_test"
	opts.SubViaHosts = []string{"panel.example"}
	res, err := Generate(services, nil, nil, map[string][]subscription.Node{"vpn_url_test": nil}, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, ok := subViaRule(t, res.JSON, "panel.example"); ok {
		t.Error("empty via-pool should emit no sub-route rule (fail-safe to direct)")
	}

	// No via-pool configured -> byte-identical to plain generation (opt-in).
	plainOpts := DefaultOptions()
	plain, err := Generate(services, nil, []subscription.Node{hy2}, map[string][]subscription.Node{"vpn_url_test": {hy2}}, plainOpts)
	if err != nil {
		t.Fatalf("generate plain: %v", err)
	}
	offOpts := DefaultOptions()
	offOpts.SubViaHosts = []string{"panel.example"} // hosts but no pool -> off
	off, err := Generate(services, nil, []subscription.Node{hy2}, map[string][]subscription.Node{"vpn_url_test": {hy2}}, offOpts)
	if err != nil {
		t.Fatalf("generate off: %v", err)
	}
	if !bytes.Equal(plain.JSON, off.JSON) {
		t.Error("via-hosts without a via-pool must not change output (opt-in, byte-identical)")
	}
}
