package config

import (
	"os"
	"path/filepath"
	"testing"
)

// One -config has to reach both schemas, chosen by the document itself. Two entry
// points is the shape that produced every "which file is authoritative" question:
// both valid, neither obviously right.
func TestTheFileDecidesItsOwnSchema(t *testing.T) {
	dir := t.TempDir()

	// No version field at all: the old schema. This direction is deliberate, so a file
	// that has always been read as v1 keeps being read as v1.
	v1 := filepath.Join(dir, "v1.yaml")
	if err := os.WriteFile(v1, []byte("singbox_version: \"1.12.17\"\nservices:\n  - name: x\n    probe_target: https://x.example/\n    chain:\n      - { state: VPN, class: vpn, strategy_id: vpn_url_test }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadAny(v1, "", "")
	if err != nil {
		t.Fatalf("a v1 document was refused: %v", err)
	}
	if _, ok := cfg.Registry.Services["x"]; !ok {
		t.Error("the v1 document did not produce its service")
	}

	// An explicit version: 1 must be accepted by the strict decoder, which rejects
	// every key the schema does not know. That is why the field had to be added to
	// BOTH parsers rather than only to the new one.
	v1v := filepath.Join(dir, "v1-versioned.yaml")
	if err := os.WriteFile(v1v, []byte("version: 1\nservices:\n  - name: x\n    probe_target: https://x.example/\n    chain:\n      - { state: VPN, class: vpn, strategy_id: vpn_url_test }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := LoadAny(v1v, "", ""); err != nil {
		t.Errorf("a v1 document declaring version 1 was refused: %v", err)
	} else if _, ok := cfg.Registry.Services["x"]; !ok {
		t.Error("version: 1 was accepted but the document lost its service")
	}

	// A v2 document, with its rules beside it.
	v2dir := filepath.Join(dir, "v2")
	if err := os.MkdirAll(filepath.Join(v2dir, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2dir, "configuration.yaml"), []byte(
		"version: 2\npools:\n  vpn_url_test: { type: url_test, caps: [tcp] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2dir, "rules", "x.yaml"), []byte(
		"id: x\nname: x\nenabled: true\nmatch: { domains: [x.example] }\n"+
			"probe: { type: http, target: \"https://x.example/\" }\n"+
			"chain:\n  - tier: preferred\n    vpn: { pool: vpn_url_test }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = LoadAny(filepath.Join(v2dir, "configuration.yaml"), "", "")
	if err != nil {
		t.Fatalf("a v2 document was refused: %v", err)
	}
	if _, ok := cfg.Registry.Services["x"]; !ok {
		t.Error("the v2 document did not produce its rule")
	}
}

// A document from a NEWER build must not load and behave like an older one. Guessing
// here produces the worst outcome in the set: a config that loads, routes, and
// silently ignores whatever the new version added.
func TestAFutureVersionIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.yaml")
	if err := os.WriteFile(path, []byte("version: 3\nservices: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadAny(path, "", "")
	if err == nil {
		t.Fatal("a document declaring version 3 was read by a build that knows 1 and 2")
	}
	for _, want := range []string{"3", "1 and 2"} {
		if !contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOfStr(haystack, needle) >= 0)
}

func indexOfStr(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
