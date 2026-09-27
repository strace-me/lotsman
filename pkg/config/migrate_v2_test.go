package config

import (
	"strings"
	"testing"
)

const v1Sample = `
services:
  - name: YouTube
    priority: 100
    probe_type: http
    probe_target: https://www.youtube.com/generate_204
    volume_target: https://www.youtube.com/videoplayback
    volume_bytes: 65536
    rule_sets: [geosite-youtube]
    domains: [googlevideo.com]
    chain:
      - { state: PREFERRED, class: zapret }
      - { state: VPN, class: vpn, strategy_id: vpn_url_test_udp }
      - { state: EMERGENCY, class: emergency, strategy_id: emergency_pool }
  - name: Discord
    disabled: true
    category: voice
    ips: ["203.0.113.10/18"]
    exclude_domains: [cdn.example]
    chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
  - name: Broken Only
    chain:
      - { state: BROKEN, class: zapret, strategy_id: alt12 }
pools:
  vpn_url_test_udp:
    type: urltest
    filter: { caps: [hy2] }
hostlists:
  - name: my-list
    out: /var/lib/lotsman/lists/my-list.txt
    sources: [https://example.invalid/list.txt]
    domains: [manual.example]
`

func migrate(t *testing.T) *V2Migration {
	t.Helper()
	doc, err := ParseDocument([]byte(v1Sample))
	if err != nil {
		t.Fatal(err)
	}
	m, err := MigrateToV2(doc, func(string) (string, error) { return "alt12", nil })
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMigrateToV2(t *testing.T) {
	m := migrate(t)
	if m.Configuration.Version != 2 {
		t.Errorf("version = %d", m.Configuration.Version)
	}
	if _, ok := m.Configuration.Desync.Instances["nfqws1-main"]; !ok {
		t.Errorf("desync instances = %#v", m.Configuration.Desync.Instances)
	}
	if m.Configuration.Pools["vpn-url-test-udp"].Type != "urltest" {
		t.Errorf("pools = %#v", m.Configuration.Pools)
	}
	if list := m.Configuration.Lists.Domains["youtube"]; list.Source != "runetfreedom" || list.Category != "youtube" {
		t.Errorf("rule_set became %#v", list)
	}
	if list := m.Configuration.Lists.Domains["my-list"]; len(list.Files) != 1 || len(list.URLs) != 1 || list.Domains[0] != "manual.example" {
		t.Errorf("hostlist became %#v", list)
	}
	if len(m.Rules) != 2 {
		t.Fatalf("rules = %#v", m.Rules)
	}
	if m.Rules[0].ID != "youtube" || !m.Rules[0].Enabled {
		t.Errorf("first rule = %#v", m.Rules[0])
	}
	if m.Rules[0].Probe.Target != "https://www.youtube.com/generate_204" || m.Rules[0].Volume.MinBytes != 65536 {
		t.Errorf("probe/volume = %#v %#v", m.Rules[0].Probe, m.Rules[0].Volume)
	}
	if m.Rules[0].Chain[0].Action.Desync.Instance != "nfqws1-main" || m.Rules[0].Chain[0].Action.Desync.Strategy != "alt12" {
		t.Errorf("desync rung = %#v", m.Rules[0].Chain[0])
	}
	if m.Rules[0].Chain[1].Tier != "fallback" || m.Rules[0].Chain[1].Action.VPN.Pool != "vpn-url-test-udp" {
		t.Errorf("vpn rung = %#v", m.Rules[0].Chain[1])
	}
	if m.Rules[1].Enabled {
		t.Error("a disabled v1 service must migrate as enabled: false")
	}
	if !containsWarning(m.Warnings, "category") {
		t.Errorf("category was not reported: %#v", m.Warnings)
	}
	if !containsWarning(m.Warnings, "exclude") {
		t.Errorf("exclude domains were not reported: %#v", m.Warnings)
	}
}

func TestMigrateDropsRuntimeOnlyState(t *testing.T) {
	m := migrate(t)
	for _, rule := range m.Rules {
		if rule.ID == "broken-only" {
			t.Fatalf("a chain of only BROKEN rungs must not become a rule: %#v", rule)
		}
	}
}

func TestMigrateKeepsRepeatedTiers(t *testing.T) {
	doc, err := ParseDocument([]byte(`
zapret:
  instances: [{ name: nfqws1-main }]
services:
  - name: Discord
    chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test_udp }
      - { state: PREFERRED, class: zapret }
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
      - { state: EMERGENCY, class: emergency, strategy_id: emergency_pool }
`))
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := migration.Rules[0].Chain
	// The two VPN rungs are DIFFERENT pools: the UDP-native one first, then the
	// TCP one. Collapsing them into one tier loses the ordering, and merging them
	// into one pool loses the transport distinction.
	want := []string{"fallback", "preferred", "fallback", "emergency"}
	if len(chain) != len(want) {
		t.Fatalf("chain = %#v", chain)
	}
	for i, tier := range want {
		if chain[i].Tier != tier {
			t.Fatalf("chain[%d] tier = %q, want %q (%#v)", i, chain[i].Tier, tier, chain)
		}
	}
	if chain[0].Action.VPN.Pool != "vpn-url-test-udp" || chain[2].Action.VPN.Pool != "vpn-url-test" {
		t.Errorf("the two VPN pools collapsed: %#v", chain)
	}
}

func TestMigrateMakesAnUnpinnedZapretRungAuto(t *testing.T) {
	// v1 left strategy_id empty on ALT_ZAPRET rungs to mean "let the KB choose".
	// v2 has to say that out loud: a rung that named nothing used to look pinned
	// to whatever happened to be selected.
	doc, err := ParseDocument([]byte(`
zapret:
  instances: [{ name: nfqws1-main }]
services:
  - name: gaming-epic
    domains: [epicgames.com]
    chain:
      - { state: PREFERRED, class: zapret, strategy_id: alt12 }
      - { state: ALT_ZAPRET, class: zapret }
`))
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := migration.Rules[0].Chain
	if chain[0].Action.Desync.Strategy != "alt12" {
		t.Errorf("the pinned rung lost its strategy: %#v", chain[0].Action.Desync)
	}
	if chain[1].Action.Desync.Strategy != "auto" {
		t.Errorf("the unpinned rung did not become auto: %#v", chain[1].Action.Desync)
	}
}

func containsWarning(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}
	return false
}
