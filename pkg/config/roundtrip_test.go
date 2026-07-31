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
