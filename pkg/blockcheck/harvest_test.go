package blockcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnumeratedCapturesAllTried(t *testing.T) {
	out := `* port block tests ipv4 rutracker.org:443
- curl_test_https_tls12 ipv4 rutracker.org : tpws --split-pos=2
- curl_test_https_tls12 ipv4 rutracker.org : nfqws --dpi-desync=multisplit --dpi-desync-split-pos=1
!!!!! curl_test_https_tls12: working strategy found for ipv4 rutracker.org : nfqws --dpi-desync=multisplit --dpi-desync-split-pos=1 !!!!!
- curl_test_https_tls12 ipv4 rutracker.org : nfqws --dpi-desync=multisplit --dpi-desync-split-pos=2
`
	got := ParseEnumerated(out)
	if len(got) != 3 { // 2 nfqws + 1 tpws tried lines; the !!!!! line is not a "- " line
		t.Fatalf("want 3 enumerated, got %d: %+v", len(got), got)
	}
	zap := ZapretResults(got)
	if len(zap) != 2 {
		t.Fatalf("want 2 nfqws, got %d", len(zap))
	}
	if zap[0].Daemon != "nfqws" || zap[0].Args[0] != "--dpi-desync=multisplit" {
		t.Errorf("first nfqws mismatch: %+v", zap[0])
	}
}

func TestDedupCollapsesSameArgs(t *testing.T) {
	in := []Result{
		{Daemon: "nfqws", Domain: "a.com", Args: []string{"--dpi-desync=multisplit"}},
		{Daemon: "nfqws", Domain: "b.com", Args: []string{"--dpi-desync=multisplit"}}, // same args, other domain
		{Daemon: "nfqws", Domain: "a.com", Args: []string{"--hostcase"}},
	}
	got := Dedup(in)
	if len(got) != 2 {
		t.Fatalf("want 2 after dedup, got %d", len(got))
	}
	if got[0].Domain != "a.com" {
		t.Errorf("dedup should keep first occurrence, got %q", got[0].Domain)
	}
}

func TestToDefinitionsDeterministicAndScoped(t *testing.T) {
	results := []Result{
		{Test: "curl_test_https_tls12", IPV: 4, Domain: "rutracker.org", Daemon: "nfqws",
			Args: []string{"--dpi-desync=fake,multisplit", "--dpi-desync-split-pos=1"}},
		{Test: "curl_test_http", IPV: 4, Domain: "rutracker.org", Daemon: "tpws",
			Args: []string{"--hostcase"}}, // tpws dropped
	}
	defs := ToDefinitions(results)
	if len(defs) != 1 {
		t.Fatalf("want 1 def (tpws dropped), got %d", len(defs))
	}
	d := defs[0]
	if d.Class != "zapret" {
		t.Errorf("class = %q, want zapret", d.Class)
	}
	// ID encodes the desync mode (commas -> underscores) and is deterministic.
	if !strings.HasPrefix(d.ID, "disc-fake_multisplit-") {
		t.Errorf("ID = %q, want disc-fake_multisplit-<hash>", d.ID)
	}
	if got := ToDefinitions(results)[0].ID; got != d.ID {
		t.Errorf("non-deterministic ID: %q != %q", got, d.ID)
	}
	// A different arg set must get a different ID.
	other := ToDefinitions([]Result{{Daemon: "nfqws", Args: []string{"--dpi-desync=multisplit"}}})[0]
	if other.ID == d.ID {
		t.Errorf("distinct args collided on ID %q", d.ID)
	}
}

func TestParseEnumeratedAgainstRealFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sim_quick_rutracker.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	all := ParseEnumerated(string(data))
	if len(all) == 0 {
		t.Fatal("expected enumerated strategies from real fixture, got none")
	}
	// Every harvested nfqws strategy must convert to a definition with args.
	defs := ToDefinitions(all)
	for _, d := range defs {
		if len(d.NFQWSArgs) == 0 {
			t.Errorf("def %q has no args", d.ID)
		}
		if !strings.HasPrefix(d.ID, "disc-") {
			t.Errorf("def ID %q not namespaced", d.ID)
		}
	}
	t.Logf("harvested %d tried, %d distinct nfqws definitions", len(all), len(defs))
}
