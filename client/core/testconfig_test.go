package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
)

// testConfig builds a runtime Config from a document and its rules, the same way the
// service does — through the one reader, not by assembling the struct by hand.
//
// Assembling it by hand would let a test pass against a shape the loader never
// produces, which is how a test ends up green about something the daemon cannot load.
func testConfig(t *testing.T, document string, rules map[string]string) *config.Config {
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
	load, err := configv2.Load(filepath.Join(dir, "configuration.yaml"))
	if err != nil {
		t.Fatalf("the test document does not load: %v", err)
	}
	parsed, err := configv2.LoadRules(filepath.Join(dir, "rules"))
	if err != nil {
		t.Fatalf("the test rules do not load: %v", err)
	}
	cfg, _, err := config.FromV2(load, parsed)
	if err != nil {
		t.Fatalf("the test document does not project: %v", err)
	}
	return cfg
}

// oneRule is the shortest rule that still produces a chain, for the tests that are
// about the chain rather than about the document.
const oneRule = `id: youtube
name: YouTube
enabled: true
match:
  domains: [youtube.com]
probe:
  type: http
  target: https://x
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: auto}}
  - {tier: fallback, vpn: {pool: vpn_url_test}}
  - {tier: emergency, vpn: {pool: emergency_pool}}
`

// withDesyncInstance is the minimum document oneRule needs: the rule names a desync
// instance, and a rule naming an undeclared instance is refused at load.
const withDesyncInstance = `version: 2
desync:
  instances:
    nfqws1-main: { engine: nfqws1, enabled: true, capture: { tcp: [443] } }
pools: {}
`

// testConfigErr is testConfig for a candidate that is EXPECTED to be refused. The
// negative tests need the error, and building the deployment by hand in each of them
// is how one of them ends up refusing for the wrong reason.
func testConfigErr(t *testing.T, document string, rules map[string]string) (*config.Config, []string, error) {
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
	cfg, _, gaps, err := config.Load(dir, "", "")
	return cfg, gaps, err
}
