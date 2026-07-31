package core

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

// TestSingboxOptionsPassesConfigKnobsThrough guards knobs the daemon has always
// honoured but the client used to drop on the floor: a config could ask for a uTLS
// fingerprint or multiplex and silently get neither. Both are anti-fingerprinting /
// anti-parallel-handshake levers, so a regression here is invisible and expensive.
func TestSingboxOptionsPassesConfigKnobsThrough(t *testing.T) {
	conf, err := config.Parse([]byte(`
utls_fingerprint: firefox
singbox_version: 1.13.14
multiplex:
  enabled: true
  protocol: h2mux
  max_connections: 1
  min_streams: 4
  padding: true
fakeip:
  enabled: true
  inet4_range: 198.18.0.0/15
services:
  - { name: youtube, category: streaming, probe_target: "https://x" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := &Core{conf: conf, reg: conf.Registry, opts: Options{ProxyListen: "127.0.0.1:1080"}}
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
	if opts.FakeIP == nil || opts.FakeIP.Inet4Range != "198.18.0.0/15" {
		t.Errorf("FakeIP not carried through: %+v", opts.FakeIP)
	}
}

// TestSingboxOptionsOmitsUnsetKnobs keeps the absent case honest: a config that says
// nothing must not start emitting multiplex/fakeip sections (which would change every
// generated config and restart sing-box for a difference the operator never asked for).
func TestSingboxOptionsOmitsUnsetKnobs(t *testing.T) {
	conf, err := config.Parse([]byte(`
services:
  - { name: youtube, category: streaming, probe_target: "https://x" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := &Core{conf: conf, reg: conf.Registry, opts: Options{ProxyListen: "127.0.0.1:1080"}}
	opts := c.singboxOptions()

	if opts.UTLSFingerprint != "" {
		t.Errorf("UTLSFingerprint = %q, want empty when unset", opts.UTLSFingerprint)
	}
	if opts.Multiplex != nil {
		t.Errorf("Multiplex = %+v, want nil when unset", opts.Multiplex)
	}
	if opts.FakeIP != nil {
		t.Errorf("FakeIP = %+v, want nil when unset", opts.FakeIP)
	}
	// TargetVersion falls back to the generator baseline rather than empty.
	if opts.TargetVersion == "" {
		t.Error("TargetVersion should keep the generator baseline when the config is silent")
	}
}
