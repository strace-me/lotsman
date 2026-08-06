package config

import (
	"encoding/json"
	"testing"
)

// TestDocumentRoundTrips guards the property the in-app configurator relies on: a
// valid config parses into an editable Document, re-serialises to YAML, and
// re-parses cleanly (structure → YAML → validate). `sample` is from config_test.go.
func TestDocumentRoundTrips(t *testing.T) {
	d, err := ParseDocument([]byte(sample))
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	y, err := d.YAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Parse(y); err != nil {
		t.Fatalf("re-parse of round-tripped YAML failed: %v\n%s", err, y)
	}
}

// TestDocumentDNSJSONKeysRoundTrip locks the exact PascalCase keys the GUI DNS form
// binds (DnsEdit.svelte) against the config structs: the doc the GUI POSTs is JSON
// with Go field names, so a rename here would silently break the form. It mirrors
// handleSetConfig's path — JSON-decode a doc into *Document, validate, re-serialise,
// re-parse — and checks the dns block survives with its provider + failover intact.
func TestDocumentDNSJSONKeysRoundTrip(t *testing.T) {
	// Exactly the shape DnsEdit.svelte builds (see mock.js): PascalCase, one provider
	// server + one local, final/direct/strategy/fakeip, and a failover list.
	guiDoc := `{
	  "Subscriptions": [{"Name":"s","URL":"https://e/x","Format":"auto","Enabled":true}],
	  "Services": [{"Name":"yt","Category":"streaming","ProbeTarget":"https://x","Domains":["example.com"]}],
	  "DNS": {
	    "Servers": [
	      {"Name":"remote","Provider":"cloudflare","Method":"https","Detour":"vpn"},
	      {"Name":"lan","Type":"local"}
	    ],
	    "Direct":"lan","Final":"remote","Strategy":"prefer_ipv4","FakeIP":false,
	    "Failover":["cloudflare","quad9","mullvad"]
	  }
	}`
	var doc Document
	if err := json.Unmarshal([]byte(guiDoc), &doc); err != nil {
		t.Fatalf("decode GUI doc: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate GUI doc: %v", err)
	}
	y, err := doc.YAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	conf, err := Parse(y)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, y)
	}
	if conf.DNS == nil {
		t.Fatal("dns block lost in round-trip — a bound JSON key does not match a config field")
	}
	if conf.DNS.Final != "remote" || len(conf.DNS.Failover) != 3 {
		t.Errorf("dns final/failover wrong after round-trip: %+v", conf.DNS)
	}
	if conf.DNS.Servers[0].Provider != "cloudflare" || conf.DNS.Servers[0].Address != "1.1.1.1" {
		t.Errorf("provider server not expanded from the round-tripped doc: %+v", conf.DNS.Servers[0])
	}
}

// TestDocumentEngineKnobsJSONKeysRoundTrip does the same for the engine-tuning knobs
// the GUI's «Движки» and «Стратегии» sections bind (EnginesEdit/StrategiesEdit): the
// uTLS fingerprint, target sing-box version, multiplex, fakeip, and custom desync
// recipes. These are the supported way to tune the engines, so a key that stops
// matching would silently drop the operator's settings.
func TestDocumentEngineKnobsJSONKeysRoundTrip(t *testing.T) {
	guiDoc := `{
	  "Services": [{"Name":"yt","Category":"streaming","ProbeTarget":"https://x"}],
	  "UTLSFingerprint": "firefox",
	  "SingboxVersion": "1.13.14",
	  "Multiplex": {"Enabled":true,"Protocol":"h2mux","MaxConnections":1,"MinStreams":4,"Padding":true},
	  "FakeIP": {"Enabled":true,"Inet4Range":"198.18.0.0/15"},
	  "Strategies": [
	    {"ID":"my-split","Class":"zapret","NFQWSArgs":["--dpi-desync=fake,multisplit","--dpi-desync-fooling=md5sig"],"BlockTypes":["rst"],"Notes":"n"}
	  ]
	}`
	var doc Document
	if err := json.Unmarshal([]byte(guiDoc), &doc); err != nil {
		t.Fatalf("decode GUI doc: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	y, err := doc.YAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	conf, err := Parse(y)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, y)
	}
	if conf.UTLSFingerprint != "firefox" || conf.SingboxVersion != "1.13.14" {
		t.Errorf("utls/version lost: %q %q", conf.UTLSFingerprint, conf.SingboxVersion)
	}
	if conf.Multiplex == nil || conf.Multiplex.Protocol != "h2mux" || conf.Multiplex.MinStreams != 4 {
		t.Errorf("multiplex lost or wrong: %+v", conf.Multiplex)
	}
	if conf.FakeIP == nil || conf.FakeIP.Inet4Range != "198.18.0.0/15" {
		t.Errorf("fakeip lost or wrong: %+v", conf.FakeIP)
	}
	if len(conf.Strategies) != 1 || len(conf.Strategies[0].NFQWSArgs) != 2 {
		t.Fatalf("strategy lost: %+v", conf.Strategies)
	}
	if conf.Strategies[0].NFQWSArgs[0] != "--dpi-desync=fake,multisplit" {
		t.Errorf("strategy args mangled: %v", conf.Strategies[0].NFQWSArgs)
	}
}

// TestHostlistDomainsJSONKeyRoundTrips locks the PascalCase key HostlistsEdit.svelte
// binds for a pack's own domains. The GUI POSTs the Document as JSON with Go field
// names, so renaming this field would break the form silently — the config would
// still save, and the domains the operator typed would simply vanish.
func TestHostlistDomainsJSONKeyRoundTrips(t *testing.T) {
	guiDoc := `{
	  "Services": [{"Name":"yt","Category":"streaming","ProbeTarget":"https://x","Domains":["youtube.com"],"DomainLists":["mine"]}],
	  "Hostlists": [{"Name":"mine","Out":"/tmp/lotsman-test-mine.txt","Sources":[],"Exclude":[],"Domains":["bank.example","work.example"],"MinKeepRatio":0}]
	}`
	var doc Document
	if err := json.Unmarshal([]byte(guiDoc), &doc); err != nil {
		t.Fatalf("decode GUI doc: %v", err)
	}
	y, err := doc.YAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	conf, err := Parse(y)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, y)
	}
	if len(conf.Hostlists) != 1 || len(conf.Hostlists[0].Domains) != 2 {
		t.Fatalf("the pack's own domains did not survive: %+v", conf.Hostlists)
	}
	// And they reached the service that attached the pack, with no file on disk.
	got := conf.Registry.Services["yt"].Domains
	found := 0
	for _, d := range got {
		if d == "bank.example" || d == "work.example" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("service domains = %v, want the pack's two merged in", got)
	}
}
