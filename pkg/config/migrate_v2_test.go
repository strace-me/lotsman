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
	if m.Configuration.Pools["vpn_url_test_udp"].Type != "urltest" {
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
	if m.Rules[0].Chain[1].Tier != "fallback" || m.Rules[0].Chain[1].Action.VPN.Pool != "vpn_url_test_udp" {
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
	//
	// The LABEL is clamped and the POSITION is not. This v1 chain is
	// VPN, PREFERRED, VPN, EMERGENCY — a PREFERRED rung sitting after a VPN one,
	// which v1 allowed because the daemon escalates by POSITION and never read
	// `state`. v2 forbids a tier going backwards, so emitting the label verbatim
	// would produce a document its own validator rejects — observed on the real
	// config, where the migration's output failed to load.
	//
	// Clamping the label changes nothing the runtime can observe, and the
	// migration says so in a warning rather than doing it quietly.
	want := []string{"fallback", "fallback", "fallback", "emergency"}
	if len(chain) != len(want) {
		t.Fatalf("chain = %#v", chain)
	}
	for i, tier := range want {
		if chain[i].Tier != tier {
			t.Fatalf("chain[%d] tier = %q, want %q (%#v)", i, chain[i].Tier, tier, chain)
		}
	}
	// Pool names are the OPERATOR's, kept verbatim. Slugging them renames something
	// of theirs in silence; the audit on the real config caught exactly that.
	if chain[0].Action.VPN.Pool != "vpn_url_test_udp" || chain[2].Action.VPN.Pool != "vpn_url_test" {
		t.Errorf("the two VPN pools collapsed: %#v", chain)
	}
	// The desync rung survives as a desync rung: clamping its LABEL must not turn
	// it into a VPN step, which would change what the service actually does.
	if chain[1].Action.Desync == nil {
		t.Errorf("the desync rung became something else: %#v", chain[1])
	}
	// And the clamp is reported, because a silent rewrite of the operator's own
	// labels is exactly the thing they need to be told about.
	if !mentionsBackwards(migration.Warnings) {
		t.Errorf("a clamped tier was not reported: %v", migration.Warnings)
	}
}

func mentionsBackwards(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, "forbids a tier going backwards") {
			return true
		}
	}
	return false
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

// The one difference the byte-comparison of the live config leaves is ORDER
// inside a single rule's rule_set list: v2 states domain lists and ip lists in
// two fields, so domain lists are always emitted first, while v1 could interleave
// them. That cannot change what the rule does — every entry in the list maps to
// the SAME outbound, so whichever rule-set matches first routes identically.
//
// The acceptance criterion is therefore SET equality, not sequence equality, and
// asserting it is what stops "it looked the same" from standing in for a check.
func TestMigratedRuleSetsAreTheSameSetNotTheSameOrder(t *testing.T) {
	doc, err := ParseDocument([]byte(`
zapret:
  instances: [{ name: nfqws1-main }]
services:
  - name: web-blocked
    rule_sets: [geosite-ru-blocked, geoip-ru-blocked, geosite-cloudflare, geoip-cloudflare, geoip-telegram, geoip-fastly]
    chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
`))
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"geosite-ru-blocked": true, "geoip-ru-blocked": true,
		"geosite-cloudflare": true, "geoip-cloudflare": true,
		"geoip-telegram": true, "geoip-fastly": true,
	}
	got := map[string]bool{}
	for _, name := range migration.Rules[0].Match.DomainLists {
		if def, ok := migration.Configuration.Lists.Domains[name]; ok {
			got["geosite-"+def.Category] = true
		}
	}
	for _, name := range migration.Rules[0].Match.IPLists {
		if def, ok := migration.Configuration.Lists.IPs[name]; ok {
			got["geoip-"+def.Category] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("resolved %d rule-sets, want %d: %v", len(got), len(want), got)
	}
	for tag := range want {
		if !got[tag] {
			t.Errorf("rule-set %q was lost", tag)
		}
	}
	// And the two namespaces must be kept apart: a domain list and an ip list
	// sharing a name is a rule that answers one with the other's file.
	if _, ok := migration.Configuration.Lists.Domains["ru-blocked"]; !ok {
		t.Error("the domain list is missing")
	}
	if _, ok := migration.Configuration.Lists.IPs["ru-blocked"]; !ok {
		t.Error("the ip list is missing — it collapsed into the domain one")
	}
}
