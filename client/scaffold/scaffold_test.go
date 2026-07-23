package scaffold

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

// The whole promise of -init is a config that just works. If the scaffold ever
// emits something config.Parse rejects, onboarding is broken — so prove the
// round-trip, and that the builtin pools the chains reference really do get
// injected (i.e. the starter needs no hand-written pools).
func TestStarterRoundTripsThroughParse(t *testing.T) {
	out := Starter([]string{"https://example.com/sub", "https://example.com/other"})

	cfg, err := config.Parse(out)
	if err != nil {
		t.Fatalf("scaffold output does not parse: %v\n---\n%s", err, out)
	}

	for _, name := range []string{"youtube", "discord", "instagram"} {
		if _, ok := cfg.Registry.Services[name]; !ok {
			t.Errorf("service %q missing from the scaffold", name)
		}
	}
	if len(cfg.Subscriptions) != 2 {
		t.Fatalf("subscriptions = %d, want 2", len(cfg.Subscriptions))
	}
	// messaging (discord) -> vpn_url_test_udp, streaming -> vpn_url_test, both
	// -> emergency_pool. None are declared in the scaffold; all must be injected.
	for _, name := range []string{"vpn_url_test", "vpn_url_test_udp", "emergency_pool"} {
		if _, ok := cfg.Pools.Pools[name]; !ok {
			t.Errorf("pool %q not present — the starter would generate an empty group", name)
		}
	}
}

func TestStarterQuotesURLsSafely(t *testing.T) {
	// A URL with a comment character or spaces must not break the YAML.
	out := Starter([]string{"https://example.com/sub?token=a#b c"})
	if _, err := config.Parse(out); err != nil {
		t.Fatalf("awkward URL broke the scaffold: %v\n%s", err, out)
	}
}
