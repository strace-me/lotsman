package blockcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A stable, hand-built sample in the exact format blockcheck.sh emits, for exact
// assertions (the real fixture varies run-to-run because SIMULATE is random).
const sample = `* checking system
...
!!!!! curl_test_https_tls12: working strategy found for ipv4 rutracker.org : nfqws --dpi-desync=multidisorder --dpi-desync-split-pos=2 !!!!!

clearing nfqws redirection

* SUMMARY
curl_test_http ipv4 rutracker.org : tpws --hostnospace
curl_test_http ipv4 rutracker.org : nfqws --hostcase
curl_test_https_tls12 ipv4 rutracker.org : nfqws --dpi-desync=multidisorder --dpi-desync-split-pos=2

Please note this SUMMARY does not guarantee a magic pill for you to copy/paste and be happy.
`

func TestParseSummary(t *testing.T) {
	got := ParseSummary(sample)
	want := []Result{
		{Test: "curl_test_http", IPV: 4, Domain: "rutracker.org", Daemon: "tpws", Args: []string{"--hostnospace"}},
		{Test: "curl_test_http", IPV: 4, Domain: "rutracker.org", Daemon: "nfqws", Args: []string{"--hostcase"}},
		{Test: "curl_test_https_tls12", IPV: 4, Domain: "rutracker.org", Daemon: "nfqws", Args: []string{"--dpi-desync=multidisorder", "--dpi-desync-split-pos=2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSummary:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseWorking(t *testing.T) {
	got := ParseWorking(sample)
	want := []Result{
		{Test: "curl_test_https_tls12", IPV: 4, Domain: "rutracker.org", Daemon: "nfqws", Args: []string{"--dpi-desync=multidisorder", "--dpi-desync-split-pos=2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseWorking:\n got %+v\nwant %+v", got, want)
	}
}

func TestZapretResults(t *testing.T) {
	got := ZapretResults(ParseSummary(sample))
	if len(got) != 2 {
		t.Fatalf("want 2 nfqws results, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if r.Daemon != "nfqws" {
			t.Errorf("ZapretResults kept non-nfqws: %+v", r)
		}
	}
}

// TestParseRealFixture validates the parser against captured real blockcheck
// output (SIMULATE mode). SIMULATE randomizes which strategies "win", so we
// assert structure, not exact strategies.
func TestParseRealFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sim_quick_rutracker.txt"))
	if err != nil {
		t.Skipf("no real fixture: %v", err)
	}
	got := ParseSummary(string(data))
	if len(got) == 0 {
		t.Fatal("parsed zero strategies from real SUMMARY block")
	}
	for _, r := range got {
		if r.Test == "" || r.Domain != "rutracker.org" || r.IPV != 4 {
			t.Errorf("malformed result: %+v", r)
		}
		if r.Daemon != "nfqws" && r.Daemon != "tpws" {
			t.Errorf("unexpected daemon %q in %+v", r.Daemon, r)
		}
		if len(r.Args) == 0 {
			t.Errorf("result has no args: %+v", r)
		}
	}
}
