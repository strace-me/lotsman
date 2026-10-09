package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

// The whole promise of -init is a deployment that just works. If the scaffold ever
// emits something the loader refuses, onboarding is broken — so prove the round-trip
// by WRITING it and LOADING it through the one reader a service uses, rather than by
// checking the text. Marshalling and hoping would not catch a rule whose chain names
// an undeclared pool, which is exactly the class of mistake a scaffold makes.
func write(t *testing.T, dep Deployment) (*config.Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "configuration.yaml"), dep.Document, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range dep.Rules {
		if err := os.WriteFile(filepath.Join(dir, "rules", name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	load, err := config.LoadV2(dir, filepath.Join(dir, "rules"), "")
	if err != nil {
		return nil, err
	}
	cfg, _, err := config.FromV2(load.Config, load.Rules)
	return cfg, err
}

func TestStarterLoadsAndNeedsNoHandWrittenPools(t *testing.T) {
	cfg, err := write(t, Starter([]string{"https://example.com/sub", "https://example.com/other"}))
	if err != nil {
		t.Fatalf("the starter deployment does not load: %v", err)
	}
	for _, name := range []string{"youtube", "discord", "instagram"} {
		if _, ok := cfg.Registry.Services[name]; !ok {
			t.Errorf("rule %q missing from the starter", name)
		}
	}
	if len(cfg.Subscriptions) != 2 {
		t.Fatalf("subscriptions = %d, want 2", len(cfg.Subscriptions))
	}
	// The starter's chains name vpn_url_test, vpn_url_test_udp and emergency_pool
	// without declaring any of them. They are built in, and if that ever stopped being
	// true the deployment would still LOAD and generate an empty group per pool — so
	// this is the assertion that has to exist.
	for _, name := range []string{"vpn_url_test", "vpn_url_test_udp", "emergency_pool"} {
		if _, ok := cfg.Pools.Pools[name]; !ok {
			t.Errorf("pool %q not present — the starter would generate an empty group", name)
		}
	}
}

func TestStarterQuotesURLsSafely(t *testing.T) {
	// A URL with a comment character or a space must not break the YAML.
	if _, err := write(t, Starter([]string{"https://example.com/sub?token=a#b c"})); err != nil {
		t.Fatalf("an awkward URL broke the starter: %v", err)
	}
}

func TestStarterRuleNamesAVolumeTargetOnlyWhereThereIsOne(t *testing.T) {
	// The canary judges "does the path CARRY" and only a rule with a volume target can
	// be judged. youtube's probe target is a 204 with no body at all, so without this
	// the throughput dimension reads as wired while measuring the size of the endpoint.
	cfg, err := write(t, Starter(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Registry.Services["youtube"].VolumeTarget == "" {
		t.Error("youtube has no volume target, so the canary can never judge it")
	}
	if cfg.Registry.Services["discord"].VolumeTarget != "" {
		t.Error("discord carries no bulk endpoint; a target here would score a 35-byte reply")
	}
}
