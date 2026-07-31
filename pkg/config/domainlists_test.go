package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDomainListsMergeIntoTheServicesDomains(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "ru-blocked.txt")
	// The plaintext format the rebuild writes: one domain per line, comments allowed.
	if err := os.WriteFile(out, []byte("# blocked\nrutracker.org\nnnmclub.to\nyoutube.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(`
hostlists:
  - name: ru-blocked
    out: ` + out + `
    sources: ["https://example.com/list.txt"]
services:
  - name: web
    category: generic
    probe_target: https://x
    domains: [youtube.com]
    domain_lists: [ru-blocked]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.Registry.Services["web"].Domains
	want := map[string]bool{"youtube.com": true, "rutracker.org": true, "nnmclub.to": true}
	if len(got) != len(want) {
		t.Fatalf("domains = %v, want the inline one plus the list, deduped (%d)", got, len(want))
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("unexpected domain %q in %v", d, got)
		}
	}
	if got[0] != "youtube.com" {
		t.Errorf("inline domains should stay first, got %v", got)
	}
}

// TestHostlistDocJSONKeysRoundTrip locks the PascalCase keys the GUI's «Списки» and
// «Сервисы» forms bind (HostlistsEdit/ServicesEdit) — a declared pack and a service
// attaching it — through the control server's decode path.
func TestHostlistDocJSONKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "ru-blocked.txt")
	if err := os.WriteFile(out, []byte("rutracker.org\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	guiDoc := `{
	  "Hostlists": [{"Name":"ru-blocked","Out":"` + out + `","Sources":["https://example.com/l.txt"],"Exclude":[],"MinKeepRatio":0.8}],
	  "Services": [{"Name":"web","Category":"generic","ProbeTarget":"https://x","Domains":["a.com"],"DomainLists":["ru-blocked"]}]
	}`
	var doc Document
	if err := json.Unmarshal([]byte(guiDoc), &doc); err != nil {
		t.Fatalf("decode: %v", err)
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
	if len(conf.Hostlists) != 1 || conf.Hostlists[0].Name != "ru-blocked" || conf.Hostlists[0].MinKeepRatio != 0.8 {
		t.Fatalf("hostlist lost in round-trip: %+v", conf.Hostlists)
	}
	// The attachment must survive too: the service ends up with its own domain plus
	// the pack's.
	got := conf.Registry.Services["web"].Domains
	if len(got) != 2 {
		t.Fatalf("domains = %v, want the inline one plus the attached pack", got)
	}
}

func TestDomainListsRejectAnUndeclaredName(t *testing.T) {
	_, err := Parse([]byte(`
services:
  - name: web
    category: generic
    probe_target: https://x
    domain_lists: [typo-list]
`))
	if err == nil {
		t.Fatal("naming an undeclared hostlist must fail — a typo would silently route nothing")
	}
}

// A service whose only matcher is an unbuilt pack parses fine — the same deliberate
// tolerance a missing ips_file has (both are caches that legitimately start empty).
// The protection against it disarming the desync lives in zaptune, which no longer
// counts a domainless service as "uncovered".
func TestDomainListsToleratesAServiceWithOnlyAnUnbuiltPack(t *testing.T) {
	cfg, err := Parse([]byte(`
hostlists:
  - name: ru-blocked
    out: /nonexistent/dir/ru-blocked.txt
    sources: ["https://example.com/list.txt"]
services:
  - name: web
    category: generic
    probe_target: https://x
    domain_lists: [ru-blocked]
`))
	if err != nil {
		t.Fatalf("an unbuilt pack must be tolerated like a missing ips_file, got: %v", err)
	}
	if d := cfg.Registry.Services["web"].Domains; len(d) != 0 {
		t.Errorf("domains = %v, want none until the pack is fetched", d)
	}
}

func TestDomainListsStripWildcardsAndDedupCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "pack.txt")
	// What real blocklists actually contain: wildcards, and entries that differ from
	// the inline ones only by case.
	if err := os.WriteFile(out, []byte("*.foo.com\nyoutube.com\nbar.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(`
hostlists:
  - name: pack
    out: ` + out + `
    sources: ["https://example.com/l.txt"]
services:
  - name: web
    category: generic
    probe_target: https://x
    domains: [YouTube.com]
    domain_lists: [pack]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.Registry.Services["web"].Domains
	for _, d := range got {
		if strings.HasPrefix(d, "*.") {
			t.Errorf("wildcard %q survived: emitted as domain_suffix it matches nothing", d)
		}
	}
	// YouTube.com (inline) and youtube.com (pack) are the same domain.
	if len(got) != 3 {
		t.Errorf("domains = %v, want 3 (YouTube.com, foo.com, bar.com) — case-insensitive dedup", got)
	}
}

func TestDuplicateHostlistNamesAreRejected(t *testing.T) {
	_, err := Parse([]byte(`
hostlists:
  - { name: pack, out: /tmp/a.txt, sources: ["https://example.com/a.txt"] }
  - { name: pack, out: /tmp/b.txt, sources: ["https://example.com/b.txt"] }
services:
  - { name: web, category: generic, probe_target: "https://x", domains: [a.com] }
`))
	if err == nil {
		t.Fatal("duplicate hostlist names must be rejected — attachment is by name, so one silently shadows the other")
	}
}

func TestDomainListsTolerateAnUnbuiltFile(t *testing.T) {
	// A fresh install: the list is declared but the rebuild job has not fetched it
	// yet. That must not stop the client from starting — the service just runs with
	// its inline domains until the first fetch lands.
	cfg, err := Parse([]byte(`
hostlists:
  - name: ru-blocked
    out: /nonexistent/dir/ru-blocked.txt
    sources: ["https://example.com/list.txt"]
services:
  - name: web
    category: generic
    probe_target: https://x
    domains: [youtube.com]
    domain_lists: [ru-blocked]
`))
	if err != nil {
		t.Fatalf("an unbuilt list file must be tolerated, got: %v", err)
	}
	if d := cfg.Registry.Services["web"].Domains; len(d) != 1 || d[0] != "youtube.com" {
		t.Errorf("domains = %v, want just the inline one", d)
	}
}
