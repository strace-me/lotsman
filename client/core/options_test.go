package core

import "testing"

// TestSingboxOptionsPassesConfigKnobsThrough guards knobs the daemon has always
// honoured but the client used to drop on the floor: a config could ask for a uTLS
// fingerprint or multiplex and silently get neither. Both are anti-fingerprinting /
// anti-parallel-handshake levers, so a regression here is invisible and expensive.
func TestSingboxOptionsPassesConfigKnobsThrough(t *testing.T) {
	conf := testConfig(t, `version: 2
target_version: 1.13.14
utls_fingerprint: firefox
multiplex:
  enabled: true
  protocol: h2mux
  max_connections: 1
  min_streams: 4
  padding: true
dns:
  fakeip: true
  servers:
    - { name: remote, provider: cloudflare, method: https }
  final: remote
pools: {}
`, map[string]string{"youtube.yaml": oneRule})
	// Tun mode, because that is where the DNS block is emitted at all: a proxy-mode
	// sing-box has a socks inbound and captures no system DNS, so a declared resolver
	// set has nothing to serve and the generator falls back to the system one. That is
	// a real limitation of proxy mode, and testing it here in proxy mode would assert
	// nothing about the pass-through this test is for.
	c := &Core{conf: conf, reg: conf.Registry}
	opts := c.singboxOptions()

	if opts.UTLSFingerprint != "firefox" {
		t.Errorf("UTLSFingerprint = %q, want firefox", opts.UTLSFingerprint)
	}
	if opts.TargetVersion != "1.13.14" {
		t.Errorf("TargetVersion = %q, want 1.13.14 (gates version-specific knobs)", opts.TargetVersion)
	}
	if opts.Multiplex == nil {
		t.Fatal("Multiplex dropped")
	}
	if opts.Multiplex.Protocol != "h2mux" || opts.Multiplex.MinStreams != 4 || !opts.Multiplex.Padding {
		t.Errorf("Multiplex not carried through: %+v", opts.Multiplex)
	}
	// fakeip is a property of the RESOLVER SET, so it is `dns.fakeip` rather than its
	// own block. The ranges are the standard reserved ones, chosen by the operator not
	// spelled out — a generated config must not carry a range nobody asked for.
	if opts.DNS == nil {
		t.Fatal("a declared dns section was dropped")
	}
	if opts.DNS.FakeIP == nil {
		t.Error("dns.fakeip was declared and did not reach the generator")
	}
}

// TestSingboxOptionsOmitsUnsetKnobs keeps the absent case honest: a config that says
// nothing must not start emitting multiplex/fakeip sections (which would change every
// generated config and restart sing-box for a difference the operator never asked for).
func TestSingboxOptionsOmitsUnsetKnobs(t *testing.T) {
	conf := testConfig(t, "version: 2\npools: {}\n", map[string]string{"youtube.yaml": oneRule})
	c := &Core{conf: conf, reg: conf.Registry}
	opts := c.singboxOptions()

	if opts.UTLSFingerprint != "" {
		t.Errorf("UTLSFingerprint = %q, want empty when unset", opts.UTLSFingerprint)
	}
	if opts.Multiplex != nil {
		t.Errorf("Multiplex = %+v, want nil when unset", opts.Multiplex)
	}
	if opts.DNS != nil && opts.DNS.FakeIP != nil {
		t.Errorf("fakeip = %+v, want nil when the config says nothing", opts.DNS.FakeIP)
	}
	// TargetVersion falls back to the generator baseline rather than empty.
	if opts.TargetVersion == "" {
		t.Error("TargetVersion should keep the generator baseline when the config is silent")
	}
}
