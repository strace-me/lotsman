package rulesets

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fakeDecompiler counts calls and returns canned domains (or an error).
type fakeDecompiler struct {
	domains []string
	err     error
	calls   int
}

func (f *fakeDecompiler) Domains(string) ([]string, error) {
	f.calls++
	return f.domains, f.err
}

// writeSrs creates a dummy .srs at LiveDir/rule-set-geosite/<tag>.srs.
func writeSrs(t *testing.T, dir, tag string) string {
	t.Helper()
	rel, _ := geositePath(tag)
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("dummy-srs"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// geositePath mirrors singbox.RuleSetRelPath without importing it (avoid cycle).
func geositePath(tag string) (string, bool) {
	return "rule-set-geosite/" + tag + ".srs", true
}

func TestResolveReturnsDomains(t *testing.T) {
	dir := t.TempDir()
	writeSrs(t, dir, "geosite-discord")
	fd := &fakeDecompiler{domains: []string{"discord.com", "discord.gg"}}
	r := &Resolver{LiveDir: dir, PathFor: geositePath, Decompiler: fd}

	got, ok := r.Resolve("geosite-discord")
	if !ok {
		t.Fatal("expected ok=true for a present .srs")
	}
	if !reflect.DeepEqual(got, []string{"discord.com", "discord.gg"}) {
		t.Errorf("domains = %v", got)
	}
}

func TestResolveNoMappingIsUncomposed(t *testing.T) {
	r := &Resolver{LiveDir: t.TempDir(), PathFor: func(string) (string, bool) { return "", false }, Decompiler: &fakeDecompiler{}}
	if _, ok := r.Resolve("weird-tag"); ok {
		t.Error("a tag with no path mapping must resolve to (nil,false) — keep uncomposed")
	}
}

func TestResolveMissingSrsIsUncomposed(t *testing.T) {
	// LiveDir has no .srs written.
	fd := &fakeDecompiler{domains: []string{"x.com"}}
	r := &Resolver{LiveDir: t.TempDir(), PathFor: geositePath, Decompiler: fd}
	if _, ok := r.Resolve("geosite-discord"); ok {
		t.Error("a missing .srs must resolve to (nil,false)")
	}
	if fd.calls != 0 {
		t.Errorf("decompiler must not run on a missing .srs, calls=%d", fd.calls)
	}
}

func TestResolveDecompileErrorIsUncomposed(t *testing.T) {
	dir := t.TempDir()
	writeSrs(t, dir, "geosite-discord")
	fd := &fakeDecompiler{err: errors.New("boom")}
	r := &Resolver{LiveDir: dir, PathFor: geositePath, Decompiler: fd}
	if _, ok := r.Resolve("geosite-discord"); ok {
		t.Error("a decompile error must resolve to (nil,false) — never compose a partial hostlist")
	}
}

func TestResolveCachesByMtime(t *testing.T) {
	dir := t.TempDir()
	p := writeSrs(t, dir, "geosite-discord")
	fd := &fakeDecompiler{domains: []string{"discord.com"}}
	r := &Resolver{LiveDir: dir, PathFor: geositePath, Decompiler: fd}

	r.Resolve("geosite-discord")
	r.Resolve("geosite-discord")
	if fd.calls != 1 {
		t.Fatalf("second resolve must hit cache (same mtime), calls=%d want 1", fd.calls)
	}

	// Bump the .srs mtime → cache invalidated → decompile runs again.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	r.Resolve("geosite-discord")
	if fd.calls != 2 {
		t.Errorf("a swapped .srs (new mtime) must re-decompile, calls=%d want 2", fd.calls)
	}
}
