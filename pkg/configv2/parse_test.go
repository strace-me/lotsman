package configv2

import "testing"

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
    type: v2ray-geo-dat
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
