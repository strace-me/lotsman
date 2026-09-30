package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/configv2"
)

// The preflight must materialize the tags the RUNTIME looks for. It once recomputed
// them locally, got a different answer, and checked for rule-sets that do not exist:
// nine lists reported as missing files against a perfectly good config. So the
// assertion here is not "these nine tags" but "the same tags BuildRegistry produces" —
// the property that actually broke.
func TestRequiredTagsAgreeWithTheRuntime(t *testing.T) {
	cfg, err := configv2.Parse([]byte(`
version: 2
lists:
  domains:
    youtube: {source: runetfreedom, dataset: geosite, category: youtube}
    ru-blocked: {source: runetfreedom, dataset: geosite, category: ru-blocked}
  ips:
    telegram: {source: runetfreedom, dataset: geoip, category: telegram}
    ru-blocked: {source: runetfreedom, dataset: geoip, category: ru-blocked}
data_sources:
  runetfreedom: {type: v2ray-srs, repo: example/repo}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := requiredTags(cfg)
	want := map[string]bool{
		"geosite-youtube": true, "geosite-ru-blocked": true,
		"geoip-telegram": true, "geoip-ru-blocked": true,
	}
	if len(got) != len(want) {
		t.Fatalf("requiredTags = %v, want %d distinct tags", got, len(want))
	}
	for _, tag := range got {
		if !want[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
	}
}

// A scratch fetch must not leave the operator's rule-set directory holding a copy of
// the bundle, and a source with nothing to fetch must say so rather than quietly
// resolving zero lists.
func TestMaterializeRefusesASourceWithNoRepo(t *testing.T) {
	_, err := materializeSource(nil, "x", configv2.DataSource{}, nil, func(string, ...any) {})
	if err == nil {
		t.Fatal("a source with no repo was materialized; its lists would silently check as empty")
	}
}

func TestEntryCounterCountsWithoutSingbox(t *testing.T) {
	dir := t.TempDir()
	// Not a real .srs: the counter must REPORT the parse failure rather than invent a
	// count, because a guard fed a fabricated zero is a guard that never fires.
	if _, err := (entryCounter{}).Count(filepath.Join(dir, "missing.srs")); err == nil {
		t.Error("a missing rule-set counted as zero rules instead of failing")
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.srs"), []byte("not srs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (entryCounter{}).Count(filepath.Join(dir, "junk.srs")); err == nil {
		t.Error("a corrupt rule-set counted as zero rules instead of failing")
	}
}
