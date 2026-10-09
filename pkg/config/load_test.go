package config

import (
	"github.com/strace-me/lotsman/pkg/configv2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A deployment is the unit. These tests write one and load it through the ONLY reader
// a runtime has, which is the property the single-schema cutover rests on: if two
// readers existed, a document could validate in one and be refused by the other, and
// each would be green in its own tests.

func writeDeployment(t *testing.T, document string, rules map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "configuration.yaml"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range rules {
		if err := os.WriteFile(filepath.Join(dir, "rules", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const minimalDocument = `version: 2
target_version: "1.14.1"
proxy:
  engine: sing-box
  mode: tun
desync:
  instances:
    nfqws1-main:
      engine: nfqws1
      enabled: true
      capture:
        tcp: [443]
pools: {}
`

const minimalRule = `id: youtube
name: YouTube
enabled: true
match:
  domains: [youtube.com]
probe:
  type: http
  target: https://www.youtube.com/generate_204
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: auto}}
  - {tier: fallback, vpn: {pool: vpn_url_test}}
`

func TestLoadAcceptsTheDirectoryOrTheFileInside(t *testing.T) {
	dir := writeDeployment(t, minimalDocument, map[string]string{"youtube.yaml": minimalRule})
	fromDir, _, _, err := Load(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fromFile, _, _, err := Load(filepath.Join(dir, "configuration.yaml"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fromDir.Registry.Services) != len(fromFile.Registry.Services) {
		t.Fatalf("directory and file forms disagree: %d vs %d services",
			len(fromDir.Registry.Services), len(fromFile.Registry.Services))
	}
}

func TestLoadRefusesAnOlderDocumentAndSaysHowToConvertIt(t *testing.T) {
	dir := writeDeployment(t, "services:\n  - name: youtube\n", nil)
	_, _, _, err := Load(dir, "", "")
	if err == nil {
		t.Fatal("a document with no version must be refused, not read as current")
	}
	// The message has to carry the remedy: an operator holding a version-1 file needs
	// the command, not just the refusal.
	if !strings.Contains(err.Error(), "v2-migrate") {
		t.Fatalf("error %q does not name the conversion command", err)
	}
}

func TestLoadRefusesAVersionFromTheFuture(t *testing.T) {
	dir := writeDeployment(t, "version: 99\n", nil)
	_, _, _, err := Load(dir, "", "")
	if err == nil {
		t.Fatal("a version this build does not know must be an error, not a guess")
	}
	// Guessing would load a newer document and behave like an older one, which is the
	// exact failure the version field exists to prevent.
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error %q does not name the version it refused", err)
	}
}

func TestLoadRejectsAnUnknownKeyRatherThanSilentlyIgnoringIt(t *testing.T) {
	// A misspelled key is indistinguishable from leaving it out otherwise: the config
	// validates, the feature is off, and the editor would then delete the line.
	dir := writeDeployment(t, minimalDocument+"\nunknown_knob: true\n", map[string]string{"youtube.yaml": minimalRule})
	_, _, _, err := Load(dir, "", "")
	if err == nil {
		t.Fatal("an unknown key must be refused")
	}
	if !strings.Contains(err.Error(), "unknown_knob") {
		t.Fatalf("error %q does not name the offending key", err)
	}
}

func TestRuleFileMustMatchItsName(t *testing.T) {
	// The file name IS the id, so a deployment is readable by listing the directory.
	// Two files claiming one id, or a file whose name lies, is a document whose
	// contents depend on iteration order.
	dir := writeDeployment(t, minimalDocument, map[string]string{
		"wrong-name.yaml": strings.Replace(minimalRule, "id: youtube", "id: other", 1),
	})
	_, _, _, err := Load(dir, "", "")
	if err == nil {
		t.Fatal("a rule whose id does not match its file name must be refused")
	}
}

func TestGapsAreComputedFromWhatTheDocumentActuallySays(t *testing.T) {
	// A fixed gap list is wrong the moment any of those settings becomes declarable,
	// because it then claims a gap that does not exist — and a gap report that lies
	// about which gaps are open trains the reader to ignore the one place the system
	// admits what it is not doing.
	bare := &configv2.Configuration{Version: 2}
	if got := GapsFor(bare); len(got) == 0 {
		t.Fatal("a document declaring no dns and no strategies must report both")
	}
	full := &configv2.Configuration{
		Version:         2,
		DNS:             &configv2.DNSSection{Servers: []configv2.DNSSectionServer{{Name: "f", Provider: "cloudflare"}}, Final: "f"},
		UTLSFingerprint: "chrome",
		Strategies:      []configv2.Strategy{{ID: "s", Class: "zapret"}},
		Desync:          configv2.Desync{Instances: map[string]configv2.Instance{"i": {Engine: "nfqws1"}}},
	}
	for _, gap := range GapsFor(full) {
		if strings.HasPrefix(gap, "dns:") || strings.HasPrefix(gap, "utls_fingerprint:") ||
			strings.HasPrefix(gap, "strategies:") {
			t.Fatalf("a document that declares this must not be told it is missing: %s", gap)
		}
	}
}

func TestTheOverrideIsReportedBecauseItTurnsLearningOff(t *testing.T) {
	// An override is a mode, not a default, and what it costs is invisible from the
	// file: per-rule recipe learning stops, because one bundle serves every rule.
	cfg := &configv2.Configuration{Version: 2, Desync: configv2.Desync{Override: &configv2.Override{Dir: t.TempDir()}}}
	found := false
	for _, gap := range GapsFor(cfg) {
		if strings.Contains(gap, "desync.override") {
			found = true
		}
	}
	if !found {
		t.Fatalf("an override must be reported: %v", GapsFor(cfg))
	}
}

func TestDesyncInstancesDeriveDisjointQueues(t *testing.T) {
	// An operator naming several instances almost cannot pick disjoint numbers by
	// hand, and the collision that produces is silent: two engines on one nfqueue
	// steal each other's packets while every status still reads healthy.
	document := `version: 2
desync:
  instances:
    nfqws_0:
      engine: nfqws1
      enabled: true
      capture: { tcp: [443] }
    nfqws_udp:
      engine: nfqws1
      enabled: true
      capture: { udp: [50000-50100] }
`
	// The rule has to name an instance that EXISTS, which is the other half of the
	// queue property: two instances exist, so a rule pointing at a third would be
	// refused before the queues were ever compared.
	rule := strings.Replace(minimalRule, "nfqws1-main", "nfqws_0", 1)
	cfg, _, _, err := Load(writeDeployment(t, document, map[string]string{"youtube.yaml": rule}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Zapret) != 2 {
		t.Fatalf("instances = %#v", cfg.Zapret)
	}
	if cfg.Zapret[0].QNum == cfg.Zapret[1].QNum {
		t.Fatalf("derived queues collide: %d and %d", cfg.Zapret[0].QNum, cfg.Zapret[1].QNum)
	}
	if cfg.Zapret[0].QNum == 0 {
		t.Fatal("a derived queue of 0 is refused by the allocator, so the daemon would not start")
	}
}

func TestDeclaredSectionsReachTheRuntime(t *testing.T) {
	// Each of these stayed at zero through the projection while the schema had no
	// field for it. The projection is what makes a declaration mean something.
	document := `version: 2
target_version: "1.14.1"
utls_fingerprint: chrome
subscription_via_pool: vpn_url_test
desync:
  instances:
    nfqws1-main:
      engine: nfqws1
      enabled: true
      capture: { tcp: [443] }
  override:
    name: ALT12
    args_file: ` + writeArgsFile(t) + `
hostlists:
  - name: gaming
    out: ` + filepath.Join(t.TempDir(), "gaming.txt") + `
    domains: [launcher.example]
strategies:
  - { id: alt12, class: zapret, block_types: [tcp_reset], notes: "Flowseal ALT12" }
dns:
  servers:
    - { name: remote, provider: cloudflare, method: https }
  final: remote
  failover: [quad9]
  direct: remote
  direct_failover: [google]
multiplex:
  enabled: true
  protocol: smux
  max_connections: 4
pools: {}
`
	cfg, _, gaps, err := Load(writeDeployment(t, document, map[string]string{"youtube.yaml": minimalRule}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UTLSFingerprint != "chrome" {
		t.Errorf("utls = %q", cfg.UTLSFingerprint)
	}
	if cfg.SubViaPool != "vpn_url_test" {
		t.Errorf("sub via pool = %q", cfg.SubViaPool)
	}
	if cfg.SingboxVersion != "1.14.1" {
		t.Errorf("target version = %q; without it a hy2 node loses its obfs", cfg.SingboxVersion)
	}
	if len(cfg.Hostlists) != 1 || cfg.Hostlists[0].Name != "gaming" {
		t.Errorf("hostlists = %#v", cfg.Hostlists)
	}
	if len(cfg.Strategies) != 1 || cfg.Strategies[0].ID != "alt12" {
		t.Errorf("strategies = %#v", cfg.Strategies)
	}
	if cfg.DNS == nil || cfg.DNS.Final != "remote" || len(cfg.DNS.Failover) != 1 {
		t.Errorf("dns = %#v", cfg.DNS)
	}
	if len(cfg.DNS.DirectFailover) != 1 {
		t.Errorf("direct failover = %v; without it a direct rung cannot resolve off-network (LOT-83)", cfg.DNS.DirectFailover)
	}
	if cfg.Multiplex == nil || cfg.Multiplex.Protocol != "smux" {
		t.Errorf("multiplex = %#v", cfg.Multiplex)
	}
	if len(cfg.DesyncOverride) != 1 || cfg.DesyncOverride[0].Name != "ALT12" {
		t.Errorf("override = %#v", cfg.DesyncOverride)
	}
	// The override's cost is stated, not left for the operator to work out from a
	// recipe that stopped changing.
	stated := false
	for _, gap := range gaps {
		if strings.Contains(gap, "desync.override") {
			stated = true
		}
	}
	if !stated {
		t.Errorf("gaps must report the override's effect on learning: %v", gaps)
	}
}

func writeArgsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alt12.args")
	body := "# ALT12\n--dpi-desync=fake,multisplit\n--dpi-desync-ttl=4\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnOverrideThatCannotBeLoadedIsRefusedNotIgnored(t *testing.T) {
	// Silently composing when the operator asked for an override is the precondition
	// that disables a feature while its declaration says it is on — and it would also
	// be invisible, because a composed strategy and an override both look like "the
	// engine is running".
	document := `version: 2
desync:
  instances:
    nfqws1-main: { engine: nfqws1, enabled: true, capture: { tcp: [443] } }
  override:
    dir: ` + filepath.Join(t.TempDir(), "empty") + `
`
	dir := writeDeployment(t, document, map[string]string{"youtube.yaml": minimalRule})
	_, _, _, err := Load(dir, "", "")
	if err == nil {
		t.Fatal("an override directory holding no .args files must be refused")
	}
}

// Both roots generate a sing-box config from the same document, so both must derive
// the same dns block from it. This mapping used to live in the desktop client alone,
// which meant `lotsmanctl generate` emitted a config with NO dns block — so
// `sing-box check` on the CLI's output approved a deployment whose resolvers the
// service would never use. A check that validates a different config than the one that
// runs is the failure this project has an axiom about.
func TestTheDeclaredDNSSectionReachesTheGenerator(t *testing.T) {
	document := `version: 2
desync:
  instances:
    nfqws1-main: { engine: nfqws1, enabled: true, capture: { tcp: [443] } }
dns:
  servers:
    - { name: remote, provider: cloudflare, method: https }
    - { name: lan, type: local }
    - { name: desync-pub, provider: quad9, method: tls, detour: direct }
    - { name: by-name, url: "https://dns.example.net/dns-query", detour: vpn }
  final: remote
  direct: lan
  zapret: desync-pub
  strategy: prefer_ipv4
  fakeip: true
  failover: [mullvad]
  direct_failover: [google]
pools: {}
`
	cfg, _, _, err := Load(writeDeployment(t, document, map[string]string{"youtube.yaml": minimalRule}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNS == nil {
		t.Fatal("the declared dns section did not reach the runtime Config")
	}
	opts := DNSOptions(cfg.DNS, "vpn_url_test")
	if opts == nil {
		t.Fatal("a declared dns section produced no generator options")
	}
	if len(opts.Servers) != 4 {
		t.Fatalf("servers = %d, want 4", len(opts.Servers))
	}
	// "vpn" is an ALIAS for the primary pool, and it must resolve — a config carrying
	// the literal string "vpn" into sing-box is a server with no route.
	for _, s := range opts.Servers {
		if s.Tag == "by-name" && s.Detour != "vpn_url_test" {
			t.Errorf("the vpn detour alias did not resolve: %q", s.Detour)
		}
	}
	if opts.Final != "remote" || opts.Direct != "lan" || opts.Zapret != "desync-pub" {
		t.Errorf("roles = final %q direct %q zapret %q", opts.Final, opts.Direct, opts.Zapret)
	}
	// The zapret role is what a desync rung needs: that traffic leaves DIRECT, so a
	// resolver behind the tunnel cannot answer it, and the rule is then down over a
	// NAME LOOKUP while every status read says the strategy is applied.
	if opts.Zapret == "" {
		t.Error("the zapret resolver role did not reach the generator")
	}
	if opts.FakeIP == nil {
		t.Error("dns.fakeip did not reach the generator")
	}
}

func TestNoDNSSectionMeansNoGeneratorOptions(t *testing.T) {
	// Absence must stay absence: a nil here is what lets the client fall back to its own
	// default rather than emitting an empty dns block.
	// The rule names a desync instance, so the document has to declare one.
	doc := `version: 2
desync:
  instances:
    nfqws1-main: { engine: nfqws1, enabled: true, capture: { tcp: [443] } }
pools: {}
`
	cfg, _, _, err := Load(writeDeployment(t, doc, map[string]string{"youtube.yaml": minimalRule}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := DNSOptions(cfg.DNS, "vpn_url_test"); got != nil {
		t.Errorf("a document with no dns section produced %+v", got)
	}
}
