package configv2

import (
	"strings"
	"testing"
)

const sample = `
version: 2
proxy:
  engine: sing-box
  mode: tun
desync:
  instances:
    nfqws1-main:
      engine: nfqws1
      enabled: true
runtime:
  probe_interval: 15s
  refresh_interval: 10m
  wan: auto
data_sources:
  runetfreedom:
    type: v2ray-srs
    repo: runetfreedom/russia-v2ray-rules-dat
    refresh: 6h
lists:
  domains:
    example:
      source: runetfreedom
      dataset: geosite
      category: example
      domains: [manual.example]
  ips:
    example:
      source: runetfreedom
      dataset: geoip
      category: example
`

func TestParse(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy.Mode != "tun" || cfg.Runtime.ProbeInterval.Value().String() != "15s" {
		t.Fatalf("runtime = %#v", cfg.Runtime)
	}
	if cfg.Runtime.StateDir != DefaultStateDir || cfg.Runtime.RuntimeDir != DefaultRuntimeDir {
		t.Errorf("defaults not applied: %#v", cfg.Runtime)
	}
	if !cfg.Desync.Instances["nfqws1-main"].Enabled {
		t.Error("desync instance was not parsed")
	}
	if source := cfg.DataSources["runetfreedom"]; source.Refresh.Value().String() != "6h0m0s" {
		t.Errorf("data source refresh = %v", source.Refresh)
	}
	if got := cfg.Lists.Domains["example"]; got.Domains[0] != "manual.example" {
		t.Errorf("inline domains = %#v", got.Domains)
	}
}

func TestParseRejectsUnknownEngine(t *testing.T) {
	if _, err := Parse([]byte("desync:\n  instances:\n    a:\n      engine: gost\n")); err == nil {
		t.Fatal("unknown desync engine was accepted")
	}
}

func TestParseRejectsListWithoutSources(t *testing.T) {
	if _, err := Parse([]byte("lists:\n  domains:\n    example: {}\n")); err == nil {
		t.Fatal("empty list definition was accepted")
	}
}

func TestParseRejectsUnknownDataSource(t *testing.T) {
	if _, err := Parse([]byte("lists:\n  domains:\n    example:\n      source: missing\n      dataset: geosite\n      category: example\n")); err == nil {
		t.Fatal("list referencing an unknown data source was accepted")
	}
}

// Each instance installs by DELETING and replacing its nft table, so two instances
// sharing one table means one wipes the other's rules — and the victim keeps
// answering as if it were installed, which is the "desync silently absent while
// every status read reads healthy" class this project keeps paying for.
func TestTwoInstancesCannotShareAnNftTable(t *testing.T) {
	cfg := &Configuration{Desync: Desync{Instances: map[string]Instance{
		"a": {Engine: "nfqws1", Enabled: true, Table: "inet zapret"},
		"b": {Engine: "nfqws2", Enabled: true, Table: "inet zapret"},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("two instances sharing one nft table were accepted")
	}
	// Distinct tables are fine, and so is leaving the per-instance default to apply.
	cfg.Desync.Instances["b"] = Instance{Engine: "nfqws2", Enabled: true, Table: "inet zapret2"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("distinct tables were refused: %v", err)
	}
	cfg.Desync.Instances["b"] = Instance{Engine: "nfqws2", Enabled: true}
	if err := cfg.Validate(); err != nil {
		t.Errorf("per-instance defaults collided: %v", err)
	}
}

// A list is EITHER handed to sing-box whole or resolved by us — never both. Both
// together means two definitions of the same membership with nothing to say which
// one routes, and the generated config is green either way.
func TestSRSURLIsExclusiveWithMaterialisedForms(t *testing.T) {
	cfg := &Configuration{Lists: Lists{Domains: map[string]List{
		"y": {SRSURL: "https://rules.example/geosite-y.srs"},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a plain srs_url list was refused: %v", err)
	}
	cfg = &Configuration{Lists: Lists{Domains: map[string]List{
		"y": {SRSURL: "https://rules.example/geosite-y.srs", Source: "runetfreedom"},
	}}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("srs_url together with source was accepted")
	}
	// The error must NAME the field to delete, not just say "pick one".
	for _, want := range []string{"srs_url", "source"} {
		if !contains([]string{err.Error()}, want) && !containsSub(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
	// https only: sing-box fetches this itself and we add no integrity check.
	cfg = &Configuration{Lists: Lists{Domains: map[string]List{
		"y": {SRSURL: "http://rules.example/geosite-y.srs"},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Error("a plain-http srs_url was accepted")
	}
}

func containsSub(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOfSub(haystack, needle) >= 0)
}

func indexOfSub(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// A migration should be a CONDITION OF A RELEASE, not a thing the operator is
// expected to remember. A document missing a required migration fails at load with
// the id of what to run, instead of starting and quietly behaving like an older
// schema — which is the shape of every "it loaded fine but the new option did
// nothing" report this project has had.
func TestAMissingMigrationIsALoadError(t *testing.T) {
	defer SetRequiredMigrations()
	SetRequiredMigrations("2026-09-28-lists-exclusivity")
	defer SetRequiredMigrations()

	cfg := &Configuration{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a document with no applied_migrations loaded against a build that requires one")
	}
	cfg = &Configuration{AppliedMigrations: []string{"2026-09-28-lists-exclusivity"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a migrated document was refused: %v", err)
	}
	// A duplicate is a hand-edit accident worth naming: it usually means two people
	// appended the same line.
	cfg = &Configuration{AppliedMigrations: []string{"m", "m"}}
	if err := cfg.Validate(); err == nil {
		t.Error("a duplicated migration id was accepted")
	}
}

// An operator who takes a name the runtime generates does not get their own service
// — they get a collision, and the failure arrives LATER, somewhere else, as a rule
// that routes nowhere. And the error has to say WHY, because a refusal the operator
// cannot act on is a refusal they work around by guessing.
func TestReservedNamesAndPrefixesAreRefusedWithAReason(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Configuration)
		wantSub string
	}{
		{"exact name", func(c *Configuration) {
			c.Desync.Instances = map[string]Instance{"sel-probe": {Engine: "nfqws1", Enabled: true}}
		}, "probe selector"},
		{"generated prefix", func(c *Configuration) {
			c.Pools = map[string]Pool{"sel-youtube": {Type: "url_test"}}
		}, "sel-<service>"},
		{"table prefix", func(c *Configuration) {
			c.Pools = map[string]Pool{"nfqueue-mine": {Type: "url_test"}}
		}, "inet nfqueue-<instance>"},
	}
	for _, tc := range cases {
		cfg := &Configuration{}
		tc.mutate(cfg)
		err := cfg.Validate()
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: the error does not explain itself: %v", tc.name, err)
		}
	}
	// And an ordinary name is untouched — a guard that refuses everything is not a
	// guard, it is an outage with better error messages.
	cfg := &Configuration{Desync: Desync{Instances: map[string]Instance{
		"nfqws1-main": {Engine: "nfqws1", Enabled: true},
	}}, Pools: map[string]Pool{"vpn_bulk": {Type: "url_test"}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("an ordinary configuration was refused: %v", err)
	}
}
