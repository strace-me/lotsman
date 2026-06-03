package rulesets

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func upPathFor(tag string) (string, bool) {
	if strings.HasPrefix(tag, "geosite-") {
		return "rule-set-geosite/" + tag + ".srs", true
	}
	return "", false
}

// fakeCounter maps a file's (trimmed) content to an entry count.
type fakeCounter struct{ byContent map[string]int }

func (f fakeCounter) Count(p string) (int, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	return f.byContent[strings.TrimSpace(string(b))], nil
}

// fakeReleaser materializes a candidate release dir from rel-path->content.
type fakeReleaser struct {
	latest string
	files  map[string]string
}

func (f fakeReleaser) Latest(context.Context, string) (string, error) { return f.latest, nil }
func (f fakeReleaser) Fetch(context.Context, string, string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "fakerel-*")
	if err != nil {
		return "", func() {}, err
	}
	for rel, content := range f.files {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeLive(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpdaterSwapsAndReconciles(t *testing.T) {
	live := t.TempDir()
	writeLive(t, live, "rule-set-geosite/geosite-youtube.srs", "OLD")

	applied := false
	u := &Updater{
		Repo: "x/y", Required: []string{"geosite-youtube"}, LiveDir: live,
		MinRatio: 0.7, PerTag: true, PathFor: upPathFor, Log: quietLog(),
		Releaser: fakeReleaser{latest: "v2", files: map[string]string{"rule-set-geosite/geosite-youtube.srs": "NEW"}},
		Counter:  fakeCounter{byContent: map[string]int{"OLD": 1000, "NEW": 1100}},
		OnApplied: func(context.Context) error {
			applied = true
			return nil
		},
	}
	if err := u.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(live, "rule-set-geosite/geosite-youtube.srs"))
	if string(got) != "NEW" {
		t.Errorf("live file = %q, want NEW (swapped)", got)
	}
	if !applied {
		t.Error("OnApplied (reconcile) must fire after a real change")
	}
}

func TestUpdaterShrinkKeepsOld(t *testing.T) {
	live := t.TempDir()
	writeLive(t, live, "rule-set-geosite/geosite-youtube.srs", "OLD")

	applied := false
	u := &Updater{
		Repo: "x/y", Required: []string{"geosite-youtube"}, LiveDir: live,
		MinRatio: 0.7, PerTag: true, PathFor: upPathFor, Log: quietLog(),
		Releaser:  fakeReleaser{latest: "v2", files: map[string]string{"rule-set-geosite/geosite-youtube.srs": "NEW"}},
		Counter:   fakeCounter{byContent: map[string]int{"OLD": 1000, "NEW": 400}}, // 40% -> below 0.7
		OnApplied: func(context.Context) error { applied = true; return nil },
	}
	if err := u.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(live, "rule-set-geosite/geosite-youtube.srs"))
	if string(got) != "OLD" {
		t.Errorf("shrunk tag must keep OLD, got %q", got)
	}
	if applied {
		t.Error("no real change -> OnApplied must NOT fire")
	}
}

func TestUpdaterUnusableReleaseUntouched(t *testing.T) {
	live := t.TempDir() // live is EMPTY (no geosite-youtube)
	u := &Updater{
		Repo: "x/y", Required: []string{"geosite-youtube"}, LiveDir: live,
		MinRatio: 0.7, PerTag: true, PathFor: upPathFor, Log: quietLog(),
		Releaser: fakeReleaser{latest: "v2", files: map[string]string{}}, // candidate also lacks it
		Counter:  fakeCounter{byContent: map[string]int{}},
	}
	if err := u.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// required tag absent in both -> unusable -> nothing created.
	if _, err := os.Stat(filepath.Join(live, "rule-set-geosite/geosite-youtube.srs")); !os.IsNotExist(err) {
		t.Error("unusable release must not create files")
	}
}
