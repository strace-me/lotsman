package configv2

import (
	"os"
	"path/filepath"
	"testing"
)

const ruleYAML = `
id: example
name: Example
enabled: true
priority: 100
match:
  domain_lists: [example]
  domains: [manual.example]
  ip_lists: [example]
probe:
  type: http
  target: https://example.com/
volume:
  targets: [https://example.com/]
  min_bytes: 65536
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: alt12}}
  - {tier: fallback, vpn: {pool: vpn_url_test_udp}}
`

func TestParseRule(t *testing.T) {
	rule, err := ParseRule([]byte(ruleYAML))
	if err != nil {
		t.Fatal(err)
	}
	if rule.Match.DomainLists[0] != "example" || rule.Match.IPLists[0] != "example" {
		t.Fatalf("match = %#v", rule.Match)
	}
	if rule.Probe.Type != "http" || rule.Volume.MinBytes != 65536 {
		t.Fatalf("probe/volume = %#v %#v", rule.Probe, rule.Volume)
	}
	if len(rule.Chain) != 2 || rule.Chain[0].Tier != "preferred" || rule.Chain[0].Action.Desync.Strategy != "alt12" {
		t.Fatalf("chain = %#v", rule.Chain)
	}
	if rule.Chain[1].Action.VPN.Pool != "vpn_url_test_udp" {
		t.Fatalf("fallback = %#v", rule.Chain[1])
	}
}

func TestParseRuleRejectsUnknownTier(t *testing.T) {
	if _, err := ParseRule([]byte("id: a\nchain:\n  - whatever:\n      direct: true\n")); err == nil {
		t.Fatal("unknown chain tier was accepted")
	}
}

func TestParseRuleRejectsTwoActions(t *testing.T) {
	if _, err := ParseRule([]byte("id: a\nchain:\n  - preferred:\n      direct: true\n      bypass: true\n")); err == nil {
		t.Fatal("chain step with two actions was accepted")
	}
}

func TestParseRuleRejectsEmptyChain(t *testing.T) {
	if _, err := ParseRule([]byte("id: a\nmatch:\n  domains: [a.example]\n")); err == nil {
		t.Fatal("rule without a chain was accepted")
	}
}

func TestLoadRulesOrdersByPriority(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, rule string) {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(rule), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("catchall", "id: catchall\npriority: 10\nchain:\n  - {tier: fallback, vpn: {pool: vpn_a}}\n")
	write("specific", "id: specific\npriority: -10\nchain:\n  - {tier: preferred, direct: true}\n")
	write("tie-b", "id: tie-b\npriority: 0\nchain:\n  - {tier: preferred, direct: true}\n")
	write("tie-a", "id: tie-a\npriority: 0\nchain:\n  - {tier: preferred, direct: true}\n")
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("scratch"), 0o644)

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	// LOWER priority first, matching what v1 emits: a protective rule with a
	// negative priority must land before a broad catch-all, or sing-box's
	// first-match routing puts the catch-all first and a domain in both sets
	// goes the wrong way.
	want := []string{"specific", "tie-a", "tie-b", "catchall"}
	if len(rules) != len(want) {
		t.Fatalf("rules = %d, want %d", len(rules), len(want))
	}
	for i, id := range want {
		if rules[i].ID != id {
			t.Fatalf("order = %v, want %v", ids(rules), want)
		}
	}
}

func ids(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.ID)
	}
	return out
}

func TestLoadRulesRejectsIDFileNameMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.yaml"), []byte("id: example\nchain:\n  - {tier: preferred, direct: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRules(dir); err == nil {
		t.Fatal("rule id not matching its file name was accepted")
	}
}
