package config

import (
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/pools"
)

func v2Fixture() (*configv2.Configuration, []configv2.Rule) {
	cfg := &configv2.Configuration{
		Version: 2,
		Desync: configv2.Desync{Instances: map[string]configv2.Instance{
			"nfqws_0": {Engine: "nfqws1", Enabled: true},
		}},
		Pools: map[string]configv2.Pool{
			"vpn_bulk": {Type: pools.TypeSpeedtestTop, Caps: []string{"tcp"}, Warmup: true},
		},
		Devices: []configv2.Device{{Name: "ps5", Sources: []string{"192.168.1.50"}, Policy: "vpn_bulk"}},
		Subscriptions: []configv2.Subscription{
			{Name: "main", URL: "https://example.invalid/s/tok", Format: "auto", Tags: []string{"normal"}, Enabled: true},
		},
	}
	on := configv2.Rule{ID: "youtube", Name: "youtube", Enabled: true,
		Match: configv2.Match{Domains: []string{"googlevideo.com"}},
		Probe: &configv2.Probe{Type: "http", Target: "https://www.youtube.com/generate_204"},
		Chain: []configv2.ChainStep{{Tier: "preferred", Action: configv2.Action{VPN: &configv2.VPNAction{Pool: "vpn_bulk"}}},
			{Tier: "fallback", Action: configv2.Action{VPN: &configv2.VPNAction{Pool: "vpn_url_test"}}},
		}}
	off := on
	off.ID, off.Enabled = "social", false
	return cfg, []configv2.Rule{on, off}
}

func TestFromV2CarriesTheRuntimeShape(t *testing.T) {
	cfg, rules := v2Fixture()
	out, gaps, err := FromV2(cfg, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Registry.Services) != 1 {
		t.Errorf("services = %d, want 1: a disabled rule must not reach the runtime", len(out.Registry.Services))
	}
	if _, ok := out.Registry.Services["social"]; ok {
		t.Error("a disabled rule is routable")
	}
	// A disabled rule is dropped from the registry but still NAMED, or a UI has no
	// way to show it and offer to switch it back on.
	if len(out.DisabledServices) != 1 || out.DisabledServices[0] != "social" {
		t.Errorf("disabled = %v", out.DisabledServices)
	}
	if len(out.Devices) != 1 || out.Devices[0].Name != "ps5" || out.Devices[0].Policy != "vpn_bulk" {
		t.Errorf("devices = %+v", out.Devices)
	}
	if len(out.Subscriptions) != 1 || out.Subscriptions[0].URL != "https://example.invalid/s/tok" {
		t.Errorf("subscriptions = %+v", out.Subscriptions)
	}
	if len(out.Zapret) != 1 || out.Zapret[0].Name != "nfqws_0" {
		t.Errorf("zapret instances = %+v", out.Zapret)
	}
	if len(gaps) == 0 {
		t.Error("no gaps reported: a v2 file loads, the daemon starts, and the operator cannot tell what is running at defaults")
	}
}

// The failure this guards: a v2 file that declares only `vpn_bulk` must NOT delete
// the standard pools, because the emergency rung every rule falls back to lives in
// one of them — and a deleted emergency pool is discovered during an outage.
func TestFromV2KeepsBuiltinPoolsUnderAnOverlay(t *testing.T) {
	cfg, rules := v2Fixture()
	out, _, err := FromV2(cfg, rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vpn_url_test", "vpn_url_test_udp", "emergency_pool", "vpn_bulk"} {
		if _, ok := out.Pools.Pools[name]; !ok {
			t.Errorf("pool %q is missing", name)
		}
	}
	if got := out.Pools.Pools["vpn_bulk"].Type; got != pools.TypeSpeedtestTop {
		t.Errorf("vpn_bulk type = %q", got)
	}
	if !out.Pools.Pools["vpn_bulk"].Warmup {
		t.Error("vpn_bulk lost warmup")
	}
	// A pool named by a device must be marked referenced, so the builtin
	// injection does not synthesise a rival of the same name.
	if _, clash := out.Pools.Pools["vpn_bulk"]; !clash {
		t.Error("device policy pool vanished")
	}
}

// UNSET IS NOT ZERO. An unset pool interval means "let the generator pick its
// default" (5m, or 1m for a warmup pool); "0s" means "never stop probing". Writing
// the zero through turned every unset interval into a pool that probes forever —
// caught by generating the SAME config through v1 and through v2 and diffing the
// two sing-box configs byte for byte, which is the only check that could see it.
func TestFromV2LeavesAnUnsetIntervalUnset(t *testing.T) {
	cfg, rules := v2Fixture()
	// A warmup pool with no interval of its own: v1 emits the warmup default (1m).
	cfg.Pools["vpn_bulk"] = configv2.Pool{Type: "url_test", Warmup: true}
	out, _, err := FromV2(cfg, rules)
	if err != nil {
		t.Fatal(err)
	}
	pool := out.Pools.Pools["vpn_bulk"]
	if pool.Interval != "" {
		t.Errorf("interval = %q, want empty so the generator applies its default", pool.Interval)
	}
	if pool.IdleTimeout != "" {
		t.Errorf("idle_timeout = %q, want empty", pool.IdleTimeout)
	}
	if !pool.Warmup {
		t.Error("warmup was lost, so the default would be the wrong one")
	}

	// An EXPLICIT interval must survive, because there it really is 0s or 1m on
	// purpose and blanking it would discard the operator's setting.
	cfg.Pools["vpn_bulk"] = configv2.Pool{Type: "url_test", Interval: configv2.Duration(90 * time.Second)}
	out, _, err = FromV2(cfg, rules)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Pools.Pools["vpn_bulk"].Interval; got != "1m30s" {
		t.Errorf("explicit interval = %q, want 1m30s", got)
	}
}
