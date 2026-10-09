package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"gopkg.in/yaml.v3"
)

// The migration is now the ONLY supported path from a version-1 document, which makes
// it load-bearing rather than a convenience. These tests run it against the REAL
// production config rather than a tidy fixture — the working file is what everyone
// actually depends on, and it is full of the shapes a fixture never has (two VPN
// rungs in one tier, an unpinned desync rung, a catch-all with a negative-priority
// sibling).
//
// What is NOT tested here any more, and should be said plainly: the byte-for-byte
// comparison between a v1-parsed and a v2-parsed document. It was the check that
// caught the inverted `priority` sort and the hy2 node that quietly lost its obfs, and
// with one schema there is nothing to compare. The claim it protected — that the
// projection loses nothing — is now carried by this test plus the per-field tests in
// load_test.go, which is weaker. If that matters, the honest replacement is a
// generated-config golden file, checked byte for byte on every build.

func TestMigrationOfTheRealProductionConfigBuildsAndValidates(t *testing.T) {
	data := readProductionConfig(t)
	doc, err := ParseLegacyDocument(data)
	if err != nil {
		t.Fatalf("the production config does not read as a v1 document: %v", err)
	}
	// The v1 tree carried one strategy per instance, so a `class: zapret` rung with no
	// strategy_id becomes `auto`. Anything else would be a guess about which bundle this
	// box runs, and a wrong one is a desync that silently does nothing.
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatalf("migration failed on the real config: %v", err)
	}
	if len(migration.Rules) == 0 {
		t.Fatal("migration produced no rules")
	}
	// The migrated document must LOAD, not merely migrate: a conversion that produces
	// something the loader refuses is a conversion the operator has to debug twice.
	out, err := yaml.Marshal(migration.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configv2.Parse(out); err != nil {
		t.Fatalf("the migrated document does not load: %v", err)
	}
	if _, err := configv2.BuildRegistry(migration.Configuration, migration.Rules, nil, nil); err != nil {
		t.Fatalf("the migrated document does not build a registry: %v", err)
	}
}

func TestMigrationBuildsADisjointDesyncInstancePerRule(t *testing.T) {
	// One nfqws process carries every rule as a `--new` block, so an instance per rule
	// would mean an instance whose port set collides with every other one's — which
	// `zapret.Validate` refuses, and rightly: two engines on one queue steal each
	// other's packets while every status still reads healthy.
	data := readProductionConfig(t)
	doc, err := ParseLegacyDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatal(err)
	}
	instances := migration.Configuration.Desync.Instances
	if len(instances) != 1 {
		t.Fatalf("migration produced %d desync instances, want exactly 1", len(instances))
	}
	for _, rule := range migration.Rules {
		for _, step := range rule.Chain {
			if d := step.Action.Desync; d != nil {
				if _, ok := instances[d.Instance]; !ok {
					t.Errorf("rule %q names desync instance %q, which the migration did not declare",
						rule.ID, d.Instance)
				}
			}
		}
	}
}

func TestMigrationReportsTheEmergencyRungItResurrects(t *testing.T) {
	// A v1 `class: emergency` rung had NO executor, so a rule that reached it failed
	// with "no executor for class" — the incident the changelog records. v2 carries the
	// pool on the vpn action, which makes the rung reachable. That is a fix AND a
	// change of behaviour, so it has to be reported rather than discovered later as a
	// rule that suddenly routes somewhere it never did.
	data := readProductionConfig(t)
	doc, err := ParseLegacyDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEmergencyRung(doc) {
		t.Skip("the production config no longer has an emergency rung")
	}
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !warnsAboutEmergency(migration.Warnings) {
		t.Errorf("an emergency rung changes behaviour from broken to working and must be reported: %v",
			migration.Warnings)
	}
}

// sameStrategy tolerates exactly one intended difference: a v1 desync rung with no
// strategy_id means "the KB picks", which v2 spells `auto`.
func sameStrategy(want, got registry.ChainStep) bool {
	if want.StrategyID == "" && want.StrategyClass == strategy.ClassZapret {
		return got.StrategyID == configv2.AutoStrategy
	}
	return want.StrategyID == got.StrategyID
}

func chainOf(s registry.Service) []string {
	out := make([]string, 0, len(s.Chain))
	for _, step := range s.Chain {
		out = append(out, step.StrategyClass+":"+step.StrategyID)
	}
	return out
}

func hasEmergencyRung(doc *LegacyDocument) bool {
	for _, s := range doc.Services {
		for _, step := range s.Chain {
			if step.Class == strategy.ClassEmergency {
				return true
			}
		}
	}
	return false
}

func warnsAboutEmergency(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, "emergency") {
			return true
		}
	}
	return false
}

func readProductionConfig(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "examples", "r5s-production.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("production example not available: %v", err)
	}
	return data
}

// The preset is the one v1 section with no obvious v2 spelling, and dropping it looks
// like a successful migration while quietly turning per-rule composition on. That is
// the worst outcome for THIS file: composition refuses flowseal-alt12-google as
// self-shadowing, so the Google rule would lose its recipe with nothing in the report
// to say why.
func TestMigrationCarriesThePresetAsAnOverride(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte(`zapret:
  preset:
    name: ALT12
    dir: /var/lib/lotsman-presets
`))
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := migration.Configuration.Desync.Override
	if got == nil {
		t.Fatal("the preset was dropped by the migration")
	}
	if got.Dir != "/var/lib/lotsman-presets" || got.Name != "ALT12" {
		t.Fatalf("override = %+v", got)
	}
	// The cost has to be stated, because it is invisible from the file: one bundle
	// serves every rule, per-rule learning is off, and the per-rule strategy_id is
	// ignored — an operator pinning a recipe would otherwise watch nothing change.
	if !warnsAbout(migration.Warnings, "desync.override") {
		t.Errorf("the override's effect on learning must be reported: %v", migration.Warnings)
	}
}

func TestMigrationWithNoPresetDeclaresNoOverride(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte("services: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if migration.Configuration.Desync.Override != nil {
		t.Errorf("a document with no preset must not gain an override: %+v", migration.Configuration.Desync.Override)
	}
}

func warnsAbout(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

// Every v1 section the first v2 draft had no field for. The migration read only the
// handful the draft knew about, and a converted document that LOADS is exactly what
// makes a dropped section invisible: the split-DNS block went, the uTLS fingerprint
// went, and nothing said so.
func TestMigrationCarriesEverySectionItCan(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte(`
utls_fingerprint: chrome
singbox_version: 1.14.1
subscription_via_pool: vpn_url_test
pools:
  vpn_url_test: { type: url_test, filter: { caps: [tcp] } }
multiplex:
  enabled: true
  protocol: h2mux
  max_connections: 4
strategies:
  - { id: my-split, class: zapret, block_types: [tcp_reset], notes: "руками" }
devices:
  - { name: console, sources: [192.0.2.50], policy: direct }
dns:
  servers:
    - { name: remote, provider: cloudflare, method: https }
    - { name: lan, type: local }
    - { name: desync-pub, provider: quad9, method: tls, detour: direct }
  final: remote
  direct: lan
  zapret: desync-pub
  strategy: prefer_ipv4
  failover: [quad9]
  direct_failover: [google]
hostlists:
  - { name: gaming, out: /tmp/gaming.txt, domains: [launcher.example] }
zapret:
  instances:
    - { name: nfqws1-main, capture: { tcp: [443] } }
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.Configuration
	if cfg.UTLSFingerprint != "chrome" {
		t.Errorf("utls_fingerprint = %q", cfg.UTLSFingerprint)
	}
	if cfg.TargetVersion != "1.14.1" {
		t.Errorf("target_version = %q; a hy2 node loses its obfs without it", cfg.TargetVersion)
	}
	if cfg.SubViaPool != "vpn_url_test" {
		t.Errorf("subscription_via_pool = %q", cfg.SubViaPool)
	}
	if cfg.Multiplex == nil || cfg.Multiplex.Protocol != "h2mux" || cfg.Multiplex.MaxConnections != 4 {
		t.Errorf("multiplex = %#v", cfg.Multiplex)
	}
	if len(cfg.Strategies) != 1 || cfg.Strategies[0].ID != "my-split" {
		t.Errorf("strategies = %#v", cfg.Strategies)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].Name != "console" {
		t.Errorf("devices = %#v", cfg.Devices)
	}
	// DNS is the one with real state in it: two failover lists and a third resolver
	// role that only a desync rung can use.
	if cfg.DNS == nil {
		t.Fatal("the split-DNS block was dropped by the migration")
	}
	if len(cfg.DNS.Servers) != 3 || cfg.DNS.Final != "remote" || cfg.DNS.Direct != "lan" {
		t.Errorf("dns = %#v", cfg.DNS)
	}
	if cfg.DNS.Zapret != "desync-pub" {
		t.Errorf("the zapret resolver role was dropped: %q", cfg.DNS.Zapret)
	}
	if len(cfg.DNS.Failover) != 1 || len(cfg.DNS.DirectFailover) != 1 {
		t.Errorf("failover lists were dropped: %v %v", cfg.DNS.Failover, cfg.DNS.DirectFailover)
	}
}

// The legacy standalone fakeip block folds into the resolver set, and says so — because
// the RANGES do not come with it, and a converted document answering from addresses
// the operator never chose is exactly the silent change this converter exists to avoid.
func TestMigrationFoldsTheLegacyFakeIPAndSaysWhatItLost(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte(`
fakeip:
  enabled: true
  inet4_range: 198.18.0.0/15
  inet6_range: fc00::/18
  resolver: https://1.1.1.1/dns-query
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Configuration.DNS == nil || !m.Configuration.DNS.FakeIP {
		t.Fatal("fakeip did not reach the dns section")
	}
	if !warnsAbout(m.Warnings, "198.18.0.0/15") {
		t.Errorf("the dropped ranges must be named: %v", m.Warnings)
	}
}

// Categories are gone from v2 and the rules migrated with the chain they RESOLVED to,
// which is the correct behaviour — but the operator has to be told, because a category's
// whole point was that several rules shared one ladder.
func TestMigrationWarnsThatCategoriesAreGone(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte(`
categories:
  streaming:
    default_chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
services:
  - name: youtube
    category: streaming
    domains: [youtube.com]
    chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !warnsAbout(m.Warnings, "categories:") {
		t.Errorf("a document with categories must say they have no v2 equivalent: %v", m.Warnings)
	}
}

// The probe TARGET is what carries and the type only qualifies it. Gating the migration
// on the type dropped the target of every rule that relied on the default — which is
// most of them — leaving a rule with no probe at all: nothing to check it against, no
// volume verdict, and a ladder that cannot tell working from frozen.
func TestMigrationCarriesAProbeThatOnlyNamedItsTarget(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte(`
services:
  - name: youtube
    probe_target: https://www.youtube.com/generate_204
    domains: [youtube.com]
    chain:
      - { state: VPN, class: vpn, strategy_id: vpn_url_test }
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Rules) != 1 {
		t.Fatalf("rules = %#v", m.Rules)
	}
	if m.Rules[0].Probe == nil {
		t.Fatal("a rule with a probe_target migrated with no probe at all")
	}
	if m.Rules[0].Probe.Target != "https://www.youtube.com/generate_204" {
		t.Errorf("probe target = %q", m.Rules[0].Probe.Target)
	}
	// http is the schema's default, written out so the file says what it means.
	if m.Rules[0].Probe.Type != "http" {
		t.Errorf("probe type = %q, want http", m.Rules[0].Probe.Type)
	}
}

// An instance with no ports is an nft rule matching nothing: the desync engine is
// absent while every status read still reports the strategy as applied. v1 allowed it
// (the port set came from a launcher script), v2 does not — so the migration gives the
// instance a port set and says it chose it.
func TestMigrationGivesAnInstanceWithNoPortsACaptureAndSaysSo(t *testing.T) {
	doc, err := ParseLegacyDocument([]byte(`
services:
  - name: youtube
    probe_target: https://x
    domains: [youtube.com]
    chain:
      - { state: PREFERRED, class: zapret }
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := MigrateToV2(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := m.Configuration.Desync.Instances["nfqws1-main"]
	if !ok {
		t.Fatalf("no instance was materialised: %#v", m.Configuration.Desync.Instances)
	}
	if len(inst.Capture.TCP) == 0 && len(inst.Capture.UDP) == 0 {
		t.Fatal("the materialised instance has no capture, so the desync would match nothing")
	}
	if !warnsAbout(m.Warnings, "materialised") {
		t.Errorf("a port set this code chose must be announced: %v", m.Warnings)
	}
}

// The shipped example is a TEMPLATE people copy, so a break in it is a break in the
// first thing a new user runs. It also has to stay a placeholder file: real
// subscription URLs or node passwords here would be in every clone of the repository.
func TestTheRouterExampleLoads(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "router")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skip("router example not present")
	}
	load, err := LoadV2(filepath.Join(dir, "configuration.yaml"), filepath.Join(dir, "rules"), "")
	if err != nil {
		t.Fatalf("examples/router does not load: %v", err)
	}
	cfg, gaps, err := FromV2(load.Config, load.Rules)
	if err != nil {
		t.Fatalf("examples/router does not project: %v", err)
	}
	if len(cfg.Registry.Services) == 0 {
		t.Fatal("the example steers nothing, so it demonstrates nothing")
	}
	// Every rule's ladder has to resolve to something, or the example teaches a
	// configuration that starts and routes nowhere.
	for name, svc := range cfg.Registry.Services {
		if len(svc.Chain) == 0 {
			t.Errorf("rule %q has an empty ladder", name)
		}
		for i, step := range svc.Chain {
			if step.StrategyID == "" && step.StrategyClass == "" {
				t.Errorf("rule %q step %v names nothing", name, i)
			}
		}
	}
	// The desync instance must capture something. An instance with no ports installs an
	// nft rule that matches nothing: the engine is absent while every status read still
	// reports the strategy applied — which is exactly what a copied example must not do.
	for _, inst := range cfg.Zapret {
		if len(inst.Capture.TCP) == 0 && len(inst.Capture.UDP) == 0 {
			t.Errorf("desync instance %q captures no ports", inst.Name)
		}
	}
	t.Logf("%d rule(s), %d pool(s), gaps: %v", len(cfg.Registry.Services), len(cfg.Pools.Pools), gaps)
}

// Both shipped examples are TEMPLATES somebody copies, and the minimal one is the first
// thing a newcomer reads top to bottom. Both have to load, and both have to carry a
// desync instance that captures something: an instance with no ports installs an nft
// rule matching nothing, so a copied example would demonstrate a desync that is absent
// while every status read reported it applied.
func TestTheShippedExamplesLoad(t *testing.T) {
	for _, dir := range []string{"minimal", "router"} {
		t.Run(dir, func(t *testing.T) {
			root := filepath.Join("..", "..", "examples", dir)
			if _, err := os.Stat(root); os.IsNotExist(err) {
				t.Skipf("examples/%s not present", dir)
			}
			load, err := LoadV2(filepath.Join(root, "configuration.yaml"), filepath.Join(root, "rules"), "")
			if err != nil {
				t.Fatalf("examples/%s does not load: %v", dir, err)
			}
			cfg, _, err := FromV2(load.Config, load.Rules)
			if err != nil {
				t.Fatalf("examples/%s does not project: %v", dir, err)
			}
			if len(cfg.Registry.Services) == 0 {
				t.Fatalf("examples/%s steers nothing", dir)
			}
			for _, inst := range cfg.Zapret {
				if len(inst.Capture.TCP) == 0 && len(inst.Capture.UDP) == 0 {
					t.Errorf("desync instance %q captures no ports — it would desync nothing", inst.Name)
				}
			}
			// Every rung has to resolve to a pool or a strategy that exists, or the
			// example teaches a configuration that starts and routes nowhere.
			for name, svc := range cfg.Registry.Services {
				for _, step := range svc.Chain {
					if step.StrategyClass == "vpn" && step.StrategyID != "" {
						if _, ok := cfg.Pools.Pools[step.StrategyID]; !ok {
							t.Errorf("rule %q names pool %q, which nothing declares or injects", name, step.StrategyID)
						}
					}
				}
			}
		})
	}
}
