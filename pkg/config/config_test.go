package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

const sample = `
services:
  - name: youtube
    category: streaming
    probe_target: https://www.youtube.com/generate_204
    chain:
      - { state: PREFERRED, class: zapret, strategy_id: simple_fake_alt2 }
      - { state: ALT_ZAPRET, class: zapret }
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }

subscriptions:
  - { name: vpn-b, url: "https://example/sub", format: auto, tags: [normal], enabled: true }
  - { name: vpn-e, url: "https://example/wl", format: clash, tags: [emergency, slow], enabled: true }

pools:
  vpn_url_test:
    type: url_test
    filter: { caps: [tcp], tags_exclude: [emergency] }
  emergency_pool:
    type: url_test
    filter: { tags_include: [emergency] }
`

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	yt, ok := cfg.Registry.Services["youtube"]
	if !ok {
		t.Fatal("youtube service missing")
	}
	if len(yt.Chain) != 3 {
		t.Fatalf("chain len = %d, want 3", len(yt.Chain))
	}
	// Positions must be assigned by order.
	for i, step := range yt.Chain {
		if step.Position != i {
			t.Errorf("step %d position = %d", i, step.Position)
		}
	}
	if yt.Chain[1].State != registry.StateAltZapret || yt.Chain[1].StrategyID != "" {
		t.Errorf("ALT_ZAPRET step = %+v, want empty strategy_id (KB-resolved)", yt.Chain[1])
	}

	if len(cfg.Subscriptions) != 2 {
		t.Fatalf("subs = %d, want 2", len(cfg.Subscriptions))
	}
	if cfg.Subscriptions[1].Tags[0] != "emergency" {
		t.Errorf("vpn-e tags = %v", cfg.Subscriptions[1].Tags)
	}

	if _, ok := cfg.Pools.Pools["vpn_url_test"]; !ok {
		t.Error("vpn_url_test pool missing")
	}
	if cfg.Pools.Pools["emergency_pool"].Name != "emergency_pool" {
		t.Error("pool name not set from key")
	}
}

// A config that leans on the builtin category chains must get the three standard
// pools those chains name (vpn_url_test / vpn_url_test_udp / emergency_pool)
// without the operator hand-copying them — the whole point of the category
// defaults. Here nothing is declared but the service and its subscription.
func TestBuiltinPoolsAreInjectedWhenReferencedButUndeclared(t *testing.T) {
	const in = `
services:
  - name: discord
    category: messaging
    probe_target: https://discord.com/api/v9/gateway
  - name: youtube
    category: streaming
    probe_target: https://x
subscriptions:
  - { name: s, url: "https://example/sub", format: auto, tags: [normal], enabled: true }
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// messaging -> vpn_url_test_udp, streaming -> vpn_url_test, both -> emergency_pool.
	for _, name := range []string{"vpn_url_test", "vpn_url_test_udp", "emergency_pool"} {
		p, ok := cfg.Pools.Pools[name]
		if !ok {
			t.Errorf("referenced builtin pool %q was not injected", name)
			continue
		}
		if p.Name != name {
			t.Errorf("injected pool %q has Name %q", name, p.Name)
		}
	}
}

// An explicit pool declaration is authoritative: injection must never clobber the
// operator's own filter with the builtin default.
func TestDeclaredPoolIsNotOverwrittenByBuiltin(t *testing.T) {
	const in = `
services:
  - name: youtube
    category: streaming
    probe_target: https://x
subscriptions:
  - { name: s, url: "https://example/sub", format: auto, tags: [normal], enabled: true }
pools:
  vpn_url_test:
    type: url_test
    filter: { caps: [tcp], countries_include: [nl] }
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.Pools.Pools["vpn_url_test"]
	if len(got.Filter.CountriesInclude) != 1 || got.Filter.CountriesInclude[0] != "nl" {
		t.Errorf("declared pool filter was overwritten by the builtin: %+v", got.Filter)
	}
	// emergency_pool is still referenced by the streaming chain and undeclared, so
	// it must have been injected alongside the untouched user pool.
	if _, ok := cfg.Pools.Pools["emergency_pool"]; !ok {
		t.Error("emergency_pool should still be injected")
	}
}

// The shipped production-draft config must always parse and validate, so a
// broken example is caught here rather than on the box.
func TestExampleR5SParses(t *testing.T) {
	cfg, err := Load("../../examples/r5s.yaml")
	if err != nil {
		t.Fatalf("load examples/r5s.yaml: %v", err)
	}
	for _, name := range []string{"ru-direct", "youtube", "discord", "ai", "web-blocked",
		"gaming-epic", "gaming-battlenet", "social", "dev"} {
		if _, ok := cfg.Registry.Services[name]; !ok {
			t.Errorf("service %q missing from example", name)
		}
	}
	// ru-direct must be hard-direct and ordered first; web-blocked last.
	if ru := cfg.Registry.Services["ru-direct"]; !ru.DirectOnly() || ru.Priority >= 0 {
		t.Errorf("ru-direct = {DirectOnly:%v Priority:%d}, want DirectOnly + negative priority", ru.DirectOnly(), ru.Priority)
	}
	if cfg.Registry.Services["web-blocked"].Priority <= 0 {
		t.Error("web-blocked should have positive priority (emitted last)")
	}
	// gaming-epic must prefer zapret (low-ping, anti-cheat-safe), not VPN-first.
	if c := cfg.Registry.Services["gaming-epic"].Chain; len(c) == 0 || c[0].StrategyClass != "zapret" {
		t.Errorf("gaming-epic chain[0] = %+v, want zapret PREFERRED", c)
	}
	// the warmup UDP pool must be flagged warmup.
	if !cfg.Pools.Pools["vpn_url_test_udp"].Warmup {
		t.Error("vpn_url_test_udp should be warmup in the example")
	}
}

func TestParseWarmupPool(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x }
pools:
  warm: { type: url_test, warmup: true, idle_timeout: "0s", interval: "1m", filter: { caps: [tcp] } }
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := cfg.Pools.Pools["warm"]
	if !p.Warmup || p.IdleTimeout != "0s" || p.Interval != "1m" {
		t.Errorf("warm pool = %+v, want warmup/0s/1m", p)
	}
}

func TestParseRejectsBadPoolDuration(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x }
pools:
  bad: { type: url_test, interval: "soon", filter: { caps: [tcp] } }
`
	if _, err := Parse([]byte(in)); err == nil {
		t.Fatal("expected error for invalid pool interval, got nil")
	}
}

func TestParsePerRungQUICProbe(t *testing.T) {
	in := `
services:
  - name: youtube
    probe_target: https://www.youtube.com/generate_204
    chain:
      - { state: PREFERRED, class: zapret, probe_type: quic, probe_target: "https://www.youtube.com/" }
      - { state: VPN,       class: vpn,    strategy_id: vpn_url_test }
      - { state: EMERGENCY, class: emergency, strategy_id: emergency_pool }
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	steps := cfg.Registry.Services["youtube"].Chain
	if steps[0].ProbeType != "quic" || steps[0].ProbeTarget != "https://www.youtube.com/" {
		t.Errorf("rung0 probe = %q / %q, want quic / url", steps[0].ProbeType, steps[0].ProbeTarget)
	}
	if steps[1].ProbeType != "" {
		t.Errorf("vpn rung should inherit the service probe (empty), got %q", steps[1].ProbeType)
	}
}

func TestParseRejectsQUICOnVPNRung(t *testing.T) {
	in := `
services:
  - name: youtube
    probe_target: https://x
    chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test, probe_type: quic }
`
	if _, err := Parse([]byte(in)); err == nil {
		t.Fatal("expected error: probe_type quic on a vpn rung false-fails the tunnel")
	}
}

func TestParseRejectsServiceLevelQUIC(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x, probe_type: quic }
`
	if _, err := Parse([]byte(in)); err == nil {
		t.Fatal("expected error: service-level probe_type quic is per-rung only")
	}
}

func TestParseZapretInstances(t *testing.T) {
	yaml := `
services:
  - name: x
    chain: [{ state: VPN, class: vpn, strategy_id: p }]
zapret:
  instances:
    - { name: tls,   qnum: 200, capture: { tcp: [80, 443], udp: [443] }, connbytes: 12 }
    - { name: voice, qnum: 201, capture: { udp: ["19294-19344", "50000-50100"] }, connbytes: 0 }
`
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Zapret) != 2 {
		t.Fatalf("instances = %d, want 2", len(cfg.Zapret))
	}
	tls := cfg.Zapret[0]
	if tls.QNum != 200 || tls.Connbytes != 12 {
		t.Errorf("tls = %+v", tls)
	}
	// int ports stringified, range strings preserved.
	if len(tls.Capture.TCP) != 2 || tls.Capture.TCP[1] != "443" {
		t.Errorf("tls tcp = %v, want [80 443]", tls.Capture.TCP)
	}
	if cfg.Zapret[1].Capture.UDP[0] != "19294-19344" {
		t.Errorf("voice udp = %v", cfg.Zapret[1].Capture.UDP)
	}
}

func TestParseZapretRejectsCollision(t *testing.T) {
	yaml := `
services:
  - name: x
    chain: [{ state: VPN, class: vpn, strategy_id: p }]
zapret:
  instances:
    - { name: a, qnum: 200, capture: { tcp: [443] } }
    - { name: b, qnum: 201, capture: { tcp: [443] } }
`
	if _, err := Parse([]byte(yaml)); err == nil {
		t.Error("expected error: tcp 443 captured by two instances")
	}
}

func TestParseHostlists(t *testing.T) {
	in := `
services:
  - name: youtube
    chain: [{ state: VPN, class: vpn }]
hostlists:
  - name: blocked
    out: /opt/zapret-lotsman/lists/blocked.txt
    sources:
      - https://example.com/list1.txt
      - https://example.com/list2.txt
    exclude:
      - https://example.com/white.txt
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Hostlists) != 1 {
		t.Fatalf("want 1 hostlist, got %d", len(cfg.Hostlists))
	}
	h := cfg.Hostlists[0]
	if h.Name != "blocked" || h.Out != "/opt/zapret-lotsman/lists/blocked.txt" {
		t.Errorf("hostlist meta mismatch: %+v", h)
	}
	if len(h.Sources) != 2 || h.Sources[0].URL != "https://example.com/list1.txt" {
		t.Errorf("sources mismatch: %+v", h.Sources)
	}
	if len(h.Exclude) != 1 || h.Exclude[0].URL != "https://example.com/white.txt" {
		t.Errorf("exclude mismatch: %+v", h.Exclude)
	}
}

func TestServiceInheritsCategoryChain(t *testing.T) {
	// youtube declares no chain → inherits builtin "streaming" default chain.
	cfg, err := Parse([]byte("services:\n  - { name: youtube, category: streaming, probe_target: https://x }"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := cfg.Registry.Services["youtube"]
	if len(svc.Chain) != 4 {
		t.Fatalf("inherited chain len = %d, want 4 (streaming default)", len(svc.Chain))
	}
	want := []string{"PREFERRED", "ALT_ZAPRET", "VPN", "EMERGENCY"}
	for i, st := range svc.Chain {
		if st.State != want[i] {
			t.Errorf("step %d state = %q, want %q", i, st.State, want[i])
		}
		if st.Position != i {
			t.Errorf("step %d position = %d, want %d", i, st.Position, i)
		}
	}
	// messaging's VPN step must point at the udp pool.
	cfg2, _ := Parse([]byte("services:\n  - { name: discord, category: messaging, probe_target: https://x }"))
	ds := cfg2.Registry.Services["discord"].Chain
	if ds[2].StrategyID != "vpn_url_test_udp" {
		t.Errorf("messaging VPN pool = %q, want vpn_url_test_udp", ds[2].StrategyID)
	}
}

func TestExplicitChainOverridesCategory(t *testing.T) {
	cfg, err := Parse([]byte("services:\n  - name: youtube\n    category: streaming\n    chain: [{ state: VPN, class: vpn, strategy_id: p }]"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Registry.Services["youtube"].Chain) != 1 {
		t.Errorf("explicit chain not honored over category default")
	}
}

func TestConfigOverridesCategory(t *testing.T) {
	in := `
categories:
  streaming:
    required_caps: [tcp]
    default_chain:
      - { state: VPN, class: vpn, strategy_id: only_vpn }
services:
  - { name: youtube, category: streaming, probe_target: https://x }
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ch := cfg.Registry.Services["youtube"].Chain
	if len(ch) != 1 || ch[0].StrategyID != "only_vpn" {
		t.Errorf("config category override not applied: %+v", ch)
	}
}

func TestServiceDomainsAndIPs(t *testing.T) {
	in := `
services:
  - name: discord
    category: messaging
    probe_target: https://x
    domains: [discord.com, discord.media, "*.discord.gg"]
    ips: [66.22.196.0/22, "1.2.3.4"]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := cfg.Registry.Services["discord"]
	if len(svc.Domains) != 3 {
		t.Errorf("domains = %v", svc.Domains)
	}
	// bare IP normalized to /32.
	if len(svc.IPs) != 2 || svc.IPs[1] != "1.2.3.4/32" {
		t.Errorf("ips = %v, want [66.22.196.0/22 1.2.3.4/32]", svc.IPs)
	}
}

func TestServiceExcludeDomains(t *testing.T) {
	in := `
services:
  - name: gaming-epic
    category: gaming
    probe_target: https://x
    domains: [fortnite.com]
    exclude_domains: [epicgames-download1.akamaized.net, download.eac-cdn.com]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := cfg.Registry.Services["gaming-epic"]
	if len(svc.ExcludeDomains) != 2 || svc.ExcludeDomains[0] != "epicgames-download1.akamaized.net" {
		t.Errorf("exclude_domains = %v, want the two raw-pass CDNs", svc.ExcludeDomains)
	}
}

func TestServiceIPsFileMerge(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/voice.cidr"
	if err := os.WriteFile(path, []byte("# voice seed\n35.217.8.0/23\n\n34.0.192.0/20\n66.22.196.0/22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := "services:\n  - name: discord\n    category: messaging\n    probe_target: https://x\n    ips: [66.22.196.0/22]\n    ips_file: " + path + "\n"
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Registry.Services["discord"].IPs
	// inline ip kept; file CIDRs appended; the duplicate 66.22.196.0/22 not doubled.
	want := []string{"66.22.196.0/22", "35.217.8.0/23", "34.0.192.0/20"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ips = %v, want %v", got, want)
	}
}

func TestServiceIPsFileMissingIsOK(t *testing.T) {
	in := "services:\n  - { name: discord, category: messaging, probe_target: https://x, ips_file: /no/such/file.cidr }"
	if _, err := Parse([]byte(in)); err != nil {
		t.Errorf("missing ips_file should be tolerated, got %v", err)
	}
}

func TestServiceIPsFileBadCIDR(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bad.cidr"
	os.WriteFile(path, []byte("not-an-ip\n"), 0o644)
	in := "services:\n  - { name: discord, category: messaging, probe_target: https://x, ips_file: " + path + " }"
	if _, err := Parse([]byte(in)); err == nil {
		t.Error("malformed CIDR in ips_file should error")
	}
}

func TestServiceStickyAndProfile(t *testing.T) {
	cfg, err := Parse([]byte("services:\n  - { name: youtube, category: streaming, sticky: true, profile: voice }"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := cfg.Registry.Services["youtube"]
	if !svc.Sticky || svc.Profile != "voice" {
		t.Errorf("sticky=%v profile=%q, want true/voice", svc.Sticky, svc.Profile)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"empty services":       `pools: {}`,
		"empty chain":          "services:\n  - { name: x, chain: [] }",
		"unknown state":        "services:\n  - name: x\n    chain:\n      - { state: WAT, class: zapret }",
		"unknown class":        "services:\n  - name: x\n    chain:\n      - { state: VPN, class: wat }",
		"unknown cap":          "services:\n  - name: x\n    chain:\n      - { state: VPN, class: vpn }\npools:\n  p:\n    filter: { caps: [bogus] }",
		"duplicate svc":        "services:\n  - name: x\n    chain: [{ state: VPN, class: vpn }]\n  - name: x\n    chain: [{ state: VPN, class: vpn }]",
		"no chain unknown cat": "services:\n  - { name: x, category: nope }",
		"bad category step":    "categories:\n  c: { default_chain: [{ state: WAT, class: vpn }] }\nservices:\n  - { name: x, category: c }",
		"bad service profile":  "services:\n  - { name: x, category: generic, profile: turbo }",
		"bad service ip":       "services:\n  - { name: x, category: generic, ips: [nope] }",
		"bad service domain":   "services:\n  - { name: x, category: generic, domains: [\"has space\"] }",
		"hostlist no out":      "services:\n  - name: x\n    chain: [{ state: VPN, class: vpn }]\nhostlists:\n  - name: blocked\n    sources: [http://a/l.txt]",
		"hostlist no src":      "services:\n  - name: x\n    chain: [{ state: VPN, class: vpn }]\nhostlists:\n  - name: blocked\n    out: /tmp/b.txt",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Errorf("expected error for %q, got nil", name)
			} else if !strings.HasPrefix(err.Error(), "config:") {
				t.Errorf("error not from config layer: %v", err)
			}
		})
	}
}

// LOT-23 group lever: a service without its own profile/sticky inherits the
// category's defaults; an explicit per-service value overrides.
func TestCategoryProfileStickyInheritance(t *testing.T) {
	in := `
categories:
  streaming:
    profile: streaming
    sticky: true
    default_chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
services:
  - name: youtube
    category: streaming
    probe_target: https://x
    domains: [googlevideo.com]
  - name: twitch
    category: streaming
    probe_target: https://x
    domains: [twitch.tv]
    profile: general
    sticky: false
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	yt := cfg.Registry.Services["youtube"]
	if yt.Profile != "streaming" || !yt.Sticky {
		t.Errorf("youtube must inherit category profile=streaming sticky=true, got profile=%q sticky=%v", yt.Profile, yt.Sticky)
	}
	tw := cfg.Registry.Services["twitch"]
	if tw.Profile != "general" || tw.Sticky {
		t.Errorf("twitch explicit profile=general sticky=false must override category, got profile=%q sticky=%v", tw.Profile, tw.Sticky)
	}
}

// LOT-23: spread_clients loads through config -> registry.Service.
func TestServiceSpreadClients(t *testing.T) {
	in := `
services:
  - name: youtube
    category: streaming
    probe_target: https://x
    rule_sets: [geosite-youtube]
    spread_clients: ["192.168.1.50/32", "192.168.1.60/32"]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := cfg.Registry.Services["youtube"]
	if len(svc.SpreadClients) != 2 || svc.SpreadClients[0] != "192.168.1.50/32" {
		t.Errorf("spread_clients = %v, want the two client CIDRs", svc.SpreadClients)
	}
}

// A misspelled key used to be indistinguishable from an absent one: the config
// validated, the feature was silently off, and — because the in-app editor
// round-trips through Document.YAML(), which serialises only known fields — saving
// from the GUI would then DELETE the misspelled line from the operator's file.
func TestParseRejectsUnknownKeys(t *testing.T) {
	cases := map[string]string{
		"typo at the top level": "subscription:\n  - { name: s, url: \"https://e/x\" }\n",
		"typo in a service":     "services:\n  - { name: yt, category: streaming, probe_target: \"https://x\", domain_list: [a] }\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: must be rejected, not silently ignored", name)
		}
	}
	// An empty document must still reach the normal validation ("no services defined")
	// rather than dying in the decoder — strictness must not change what "empty" means.
	if _, err := Parse(nil); err == nil || !strings.Contains(err.Error(), "no services") {
		t.Errorf("an empty config should fail validation, not decoding; got %v", err)
	}
}

// A switched-off service must leave the registry entirely, because every consumer
// — prober, brain, route generator, desync composer — reads the registry. Leaving
// it in with a flag would mean one forgotten guard is a service that is "off" but
// still probed and still routed.
func TestParseDropsDisabledServiceFromTheRegistry(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x }
  - { name: discord, category: messaging, probe_target: https://y, disabled: true }
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := cfg.Registry.Services["discord"]; ok {
		t.Error("a disabled service must not be in the registry — every consumer reads it")
	}
	if _, ok := cfg.Registry.Services["youtube"]; !ok {
		t.Error("the enabled service went missing")
	}
	if len(cfg.DisabledServices) != 1 || cfg.DisabledServices[0] != "discord" {
		t.Errorf("DisabledServices = %v, want [discord] so a UI can offer to switch it back on", cfg.DisabledServices)
	}
}

// Being switched off is not a licence to be malformed: the typo should surface now,
// not at the moment someone switches the service back on and the whole config stops
// loading.
func TestParseStillValidatesADisabledService(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x }
  - { name: discord, category: messaging, probe_target: https://y, disabled: true, profile: nonsense }
`
	if _, err := Parse([]byte(in)); err == nil {
		t.Fatal("expected the bad profile on a disabled service to be rejected")
	}
}

func TestParseRejectsEveryServiceDisabled(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x, disabled: true }
`
	_, err := Parse([]byte(in))
	if err == nil {
		t.Fatal("expected an error: with every service off there is nothing to steer")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should name the cause, got %q", err)
	}
}

// The duplicate check must span disabled entries too, or a second declaration of a
// name lies dormant until someone switches it on.
func TestParseRejectsDuplicateWhenOneIsDisabled(t *testing.T) {
	in := `
services:
  - { name: youtube, category: streaming, probe_target: https://x }
  - { name: youtube, category: streaming, probe_target: https://z, disabled: true }
`
	if _, err := Parse([]byte(in)); err == nil {
		t.Fatal("expected duplicate service to be rejected even when the second is disabled")
	}
}

// richSample exercises most sections at once, so the toggle test below is a real
// check that the write-back preserves a config rather than a config with one key.
const richSample = `
utls_fingerprint: chrome
singbox_version: "1.13.14"
subscription_via_pool: vpn_url_test

subscriptions:
  - { name: vpn-b, url: "https://example/sub", format: auto, tags: [normal], enabled: true }
  - { name: vpn-e, url: "https://example/wl", format: clash, tags: [emergency, slow], enabled: true }

categories:
  custom:
    required_caps: [tcp]
    profile: streaming
    sticky: true
    default_chain:
      - { state: PREFERRED, class: zapret }
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }

pools:
  vpn_url_test:
    type: url_test
    filter: { caps: [tcp], tags_exclude: [emergency] }
    warmup: true
    interval: 1m
    idle_timeout: 30m

strategies:
  - id: my_recipe
    class: zapret
    nfqws_args: ["--dpi-desync=fake", "--dpi-desync-ttl=4"]
    block_types: [tcp_reset]
    notes: hand-tuned

hostlists:
  - name: pack
    out: /tmp/lotsman-test-pack.txt
    sources: ["https://example/list.txt"]
    exclude: ["https://example/allow.txt"]
    min_keep_ratio: 0.5

devices:
  - { name: tv, sources: ["192.168.1.50"], policy: strict }

zapret:
  instances:
    - name: main
      qnum: 200
      connbytes: 6
      capture: { tcp: [80, 443], udp: [443] }

dns:
  servers:
    - { name: remote, provider: cloudflare, method: https, detour: vpn }
    - { name: lan, type: local }
  direct: lan
  final: remote
  strategy: prefer_ipv4
  failover: [cloudflare, quad9]

multiplex:
  enabled: true
  protocol: smux
  max_connections: 4
  padding: true

services:
  - name: youtube
    category: streaming
    probe_target: https://www.youtube.com/generate_204
    domains: [youtube.com, googlevideo.com]
    exclude_domains: [redirector.googlevideo.com]
    ips: ["1.2.3.0/24"]
    priority: -10
    tls_fragment: true
    sticky: false
    escalate_after: 4
    recover_after: 7
    chain:
      - { state: PREFERRED, class: zapret, strategy_id: simple_fake_alt2 }
      - { state: ALT_ZAPRET, class: zapret }
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
  - name: discord
    category: custom
    probe_type: tcp
    probe_target: discord.com:443
    domains: [discord.com]
    domain_lists: [pack]
    spread_clients: ["192.168.1.0/24"]
    static: true
    profile: voice
`

// Switching a service off and back on must leave the configuration exactly as it
// was — not merely equivalent, but the same bytes. A tile toggle that quietly
// reformats the operator's file, expanding zero values they never wrote, is a
// worse bug than the feature is a feature.
func TestServiceToggleIsLosslessOverARichConfig(t *testing.T) {
	before, err := Parse([]byte(richSample))
	if err != nil {
		t.Fatalf("parse rich sample: %v", err)
	}
	off, found, err := SetServiceEnabledYAML([]byte(richSample), "discord", false)
	if err != nil || !found {
		t.Fatalf("switch off: found=%v err=%v", found, err)
	}
	offCfg, err := Parse(off)
	if err != nil {
		t.Fatalf("re-parse with discord off: %v\n%s", err, off)
	}
	if _, ok := offCfg.Registry.Services["discord"]; ok {
		t.Error("discord survived being switched off")
	}
	if len(offCfg.Registry.Services) != 1 {
		t.Errorf("registry has %d services with discord off, want 1", len(offCfg.Registry.Services))
	}

	on, found, err := SetServiceEnabledYAML(off, "discord", true)
	if err != nil || !found {
		t.Fatalf("switch on: found=%v err=%v", found, err)
	}
	after, err := Parse(on)
	if err != nil {
		t.Fatalf("re-parse after switching back on: %v\n%s", err, on)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("off→on changed the parsed configuration")
	}
	// The whole point of editing the node instead of the document: switching on
	// removes the key, so the text returns to what it was.
	if string(on) != string(mustReindent(t, richSample)) {
		t.Errorf("off→on did not restore the file text.\n--- got ---\n%s\n--- want ---\n%s", on, mustReindent(t, richSample))
	}
}

// mustReindent runs the sample through the same encoder without changing anything,
// which is the fair comparison: the encoder normalises indentation and drops the
// leading blank line, and that normalisation is not what this test is about.
func mustReindent(t *testing.T, in string) []byte {
	t.Helper()
	out, found, err := SetServiceEnabledYAML([]byte(in), "youtube", true) // already on = no-op edit
	if err != nil || !found {
		t.Fatalf("reindent: found=%v err=%v", found, err)
	}
	return out
}

// Comments are the reason this edit is surgical. A hand-maintained config must
// come back with its notes intact.
func TestServiceToggleKeepsComments(t *testing.T) {
	in := `# top of file — why this config exists
services:
  # youtube is the canary: if it breaks, everything has
  - name: youtube
    category: streaming
    probe_target: https://x   # generate_204
  - name: discord
    category: messaging
    probe_target: https://y
`
	off, found, err := SetServiceEnabledYAML([]byte(in), "discord", false)
	if err != nil || !found {
		t.Fatalf("switch off: found=%v err=%v", found, err)
	}
	for _, want := range []string{
		"# top of file — why this config exists",
		"# youtube is the canary",
		"# generate_204",
	} {
		if !strings.Contains(string(off), want) {
			t.Errorf("comment %q was lost:\n%s", want, off)
		}
	}
	if !strings.Contains(string(off), "disabled: true") {
		t.Errorf("the switch did not land:\n%s", off)
	}
}

func TestServiceToggleReportsAnUndeclaredService(t *testing.T) {
	_, found, err := SetServiceEnabledYAML([]byte(richSample), "nosuch", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("SetServiceEnabledYAML claimed to have switched a service that is not declared")
	}
}
