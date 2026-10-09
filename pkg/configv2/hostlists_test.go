package configv2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A rule whose domains live only in a hostlist routes correctly but DESYNCS NOTHING,
// and that is not a visible failure: sing-box matches the rule-set, every status read
// is green, and the service is still blocked. These tests pin the thing that makes
// the desync engine see the domains at all — the pack's entries landing inline in
// registry.Service.Domains, which is what the composer hands nfqws.

func TestHostlistDomainsReachTheRuleInline(t *testing.T) {
	dir := t.TempDir()
	// The file is what the rebuild job produces: the fetched half of the pack. The
	// declaration's own entries must arrive even when the file is missing, because a
	// fresh install is exactly that case.
	if err := os.WriteFile(filepath.Join(dir, "gaming.txt"),
		[]byte("# fetched\nlauncher.example\n*.wildcard.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Configuration{
		Hostlists: []Hostlist{{
			Name:         "gaming",
			Out:          filepath.Join(dir, "gaming.txt"),
			Sources:      []string{"https://example.invalid/gaming.txt"},
			Domains:      []string{"Hand-Typed.Example"},
			MinKeepRatio: 0.5,
		}},
	}
	rules := []Rule{{
		ID: "games", Name: "games", Enabled: true,
		Match: Match{DomainLists: []string{"gaming"}},
		Chain: []ChainStep{{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "vpn_url_test"}}}},
		Probe: &Probe{Type: "http", Target: "https://example.invalid/204"},
	}}

	reg, err := BuildRegistry(cfg, rules, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Services["games"]
	// Lowercased, because the hand-typed entry goes through the same parser as a
	// fetched one — otherwise it would slip past dedup and reach the generator in a
	// case nothing else uses.
	want := []string{"hand-typed.example", "launcher.example", "wildcard.example"}
	if len(got.Domains) != len(want) {
		t.Fatalf("domains = %v, want %v", got.Domains, want)
	}
	for i, d := range want {
		if got.Domains[i] != d {
			t.Fatalf("domains = %v, want %v", got.Domains, want)
		}
	}
	// The leading "*." is stripped because the generator emits domain_suffix, and
	// "*.foo.com" as a suffix matches nothing — real blocklists are full of that form.
	if got.RuleSets != nil {
		t.Fatalf("a hostlist must not become a rule-set tag: %v", got.RuleSets)
	}
}

func TestHostlistEntryWorksBeforeItsFileExists(t *testing.T) {
	// A sources-less pack has no rebuild job at all, so a file may never exist. The
	// declaration's own entries still have to reach the rule.
	cfg := &Configuration{Hostlists: []Hostlist{{
		Name:    "bank",
		Out:     filepath.Join(t.TempDir(), "never-written.txt"),
		Domains: []string{"bank.example"},
	}}}
	reg, err := BuildRegistry(cfg, []Rule{{
		ID: "bank", Enabled: true,
		Match: Match{DomainLists: []string{"bank"}},
		Chain: []ChainStep{{Action: Action{VPN: &VPNAction{Pool: "vpn_url_test"}}}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := reg.Services["bank"].Domains; len(d) != 1 || d[0] != "bank.example" {
		t.Fatalf("domains = %v, want [bank.example]", d)
	}
}

func TestDomainListAndHostlistAreOneNamespaceButNotInterchangeable(t *testing.T) {
	cfg := &Configuration{
		Lists:     Lists{Domains: map[string]List{"ru-blocked": {Category: "ru-blocked"}}},
		Hostlists: []Hostlist{{Name: "mine", Out: "/tmp/mine.txt", Domains: []string{"mine.example"}}},
	}
	// Both resolve, by name, from the same field — that is the point.
	reg, err := BuildRegistry(cfg, []Rule{{
		ID: "r", Enabled: true,
		Match: Match{DomainLists: []string{"ru-blocked", "mine"}},
		Chain: []ChainStep{{Action: Action{VPN: &VPNAction{Pool: "vpn_url_test"}}}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Services["r"]
	if len(got.RuleSets) != 1 || got.RuleSets[0] != "geosite-ru-blocked" {
		t.Fatalf("rule sets = %v, want [geosite-ru-blocked]", got.RuleSets)
	}
	if len(got.Domains) != 1 || got.Domains[0] != "mine.example" {
		t.Fatalf("domains = %v, want [mine.example]", got.Domains)
	}
}

func TestUnknownDomainListIsRefusedNotGuessed(t *testing.T) {
	// Refusing whole rather than resolving to something plausible: a typo that quietly
	// became an empty match would desync nothing and say nothing.
	cfg := &Configuration{}
	err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	r := Rule{ID: "r", Enabled: true, Match: Match{DomainLists: []string{"typo"}}}
	err = r.ValidateReferences(cfg, nil)
	if err == nil {
		t.Fatal("a name that is neither a list nor a hostlist must be refused")
	}
	// The message has to say both halves, or the reader goes looking in the wrong file.
	msg := err.Error()
	for _, want := range []string{"typo", "lists.domains", "hostlists"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %q", msg, want)
		}
	}
}

func TestDuplicateHostlistNameIsRefused(t *testing.T) {
	// Rules attach a pack BY NAME, so a duplicate silently resolves last-wins and the
	// shadowed one is fetched forever with nothing reading it.
	_, err := BuildHostlists([]Hostlist{
		{Name: "x", Out: "/tmp/x.txt", Domains: []string{"a.example"}},
		{Name: "x", Out: "/tmp/y.txt", Domains: []string{"b.example"}},
	})
	if err == nil {
		t.Fatal("duplicate hostlist names must be refused")
	}
}

func TestHostlistNeedsSomethingToBeMadeOf(t *testing.T) {
	// Four hand-written domains do not deserve a URL to be published at and fetched
	// back; a pack with neither sources nor domains is a typo, not an empty list.
	if _, err := BuildHostlist(Hostlist{Name: "x", Out: "/tmp/x.txt"}); err == nil {
		t.Fatal("a hostlist with neither sources nor domains must be refused")
	}
}
