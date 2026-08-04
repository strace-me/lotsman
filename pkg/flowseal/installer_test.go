package flowseal

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip builds an in-memory zip with the given path->content entries.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFileInstallerExtractsAndLinks(t *testing.T) {
	base := t.TempDir()
	inst := NewFileInstaller(base)

	if inst.CurrentVersion() != "" {
		t.Errorf("cold CurrentVersion = %q, want empty", inst.CurrentVersion())
	}

	raw := makeZip(t, map[string]string{
		"bin/stun.bin":           "PAYLOAD",
		"lists/list-general.txt": "youtube.com\n",
		"general (ALT12).bat":    "strategy",
	})
	if err := inst.Install(context.Background(), Release{Tag: "1.9.9a"}, raw); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Versioned dir got the files.
	dir := filepath.Join(base, "flowseal-1.9.9a")
	if b, err := os.ReadFile(filepath.Join(dir, "bin/stun.bin")); err != nil || string(b) != "PAYLOAD" {
		t.Errorf("extracted payload = %q err=%v", b, err)
	}

	// Symlink points at it, and CurrentVersion reads back the tag.
	target, err := os.Readlink(inst.CurrentLink)
	if err != nil || filepath.Base(target) != "flowseal-1.9.9a" {
		t.Errorf("symlink target = %q err=%v", target, err)
	}
	if inst.CurrentVersion() != "1.9.9a" {
		t.Errorf("CurrentVersion = %q, want 1.9.9a", inst.CurrentVersion())
	}

	// Installing a newer one repoints the symlink.
	if err := inst.Install(context.Background(), Release{Tag: "1.9.10"}, makeZip(t, map[string]string{"x": "y"})); err != nil {
		t.Fatal(err)
	}
	if inst.CurrentVersion() != "1.9.10" {
		t.Errorf("after upgrade CurrentVersion = %q, want 1.9.10", inst.CurrentVersion())
	}
}

// Newer upstream releases wrap everything in a single "<repo>-<tag>/" dir; the
// installer must strip it so lists/ lands at the root the strategy scripts expect
// (the 1.9.9c bug that wedged nfqws for days).
func TestFileInstallerStripsWrapperDir(t *testing.T) {
	base := t.TempDir()
	inst := NewFileInstaller(base)
	raw := makeZip(t, map[string]string{
		"zapret-discord-youtube-1.9.9c/lists/list-general.txt": "youtube.com\n",
		"zapret-discord-youtube-1.9.9c/bin/stun.bin":           "PAYLOAD",
	})
	if err := inst.Install(context.Background(), Release{Tag: "1.9.9c"}, raw); err != nil {
		t.Fatalf("install: %v", err)
	}
	dir := filepath.Join(base, "flowseal-1.9.9c")
	// FLAT: lists/list-general.txt at the root, NOT under the wrapper dir.
	if b, err := os.ReadFile(filepath.Join(dir, "lists/list-general.txt")); err != nil || string(b) != "youtube.com\n" {
		t.Errorf("flat lists/list-general.txt = %q err=%v (wrapper not stripped)", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "zapret-discord-youtube-1.9.9c")); !os.IsNotExist(err) {
		t.Errorf("wrapper dir should be stripped, but it exists")
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	raw := makeZip(t, map[string]string{"../escape.txt": "evil"})
	if err := unzip(raw, dir); err == nil {
		t.Error("expected zip-slip rejection")
	}
}

// The failure that has bitten three times: a release ships no -user exclude
// lists, the extract is stateless, and the strategy script that requires them
// stops starting — silently, because the nft rules carry `flags bypass`.
func TestInstallCarriesOurStateIntoTheNewBundle(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "flowseal-1.0.0")
	mustMkdir(t, filepath.Join(old, "lists"))
	mustWrite(t, filepath.Join(old, "lists", "ipset-exclude-user.txt"), "10.0.0.0/8\n")
	mustWrite(t, filepath.Join(old, "lists", "list-general.txt"), "old-shipped\n")
	link := filepath.Join(base, "flowseal-current")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}

	i := NewFileInstaller(base)
	// The new release ships list-general.txt but no -user file, like 1.10.0.
	// Two top-level entries, like a real bundle: a single one would be stripped as
	// a wrapper dir by the 1.9.9c defence and never land under lists/.
	zipped := makeZip(t, map[string]string{
		"lists/list-general.txt": "new-shipped\n",
		"general.bat":            "@echo off\n",
	})
	if err := i.Install(context.Background(), Release{Tag: "1.1.0"}, zipped); err != nil {
		t.Fatalf("install: %v", err)
	}

	newDir := filepath.Join(base, "flowseal-1.1.0")
	if got := readFile(t, filepath.Join(newDir, "lists", "ipset-exclude-user.txt")); got != "10.0.0.0/8\n" {
		t.Errorf("our exclude list was not carried forward, got %q", got)
	}
	// A file the release ships is the release's to own.
	if got := readFile(t, filepath.Join(newDir, "lists", "list-general.txt")); got != "new-shipped\n" {
		t.Errorf("carry-forward overwrote a shipped file: %q", got)
	}
	if target, _ := os.Readlink(link); target != newDir {
		t.Errorf("symlink = %s, want %s", target, newDir)
	}
}

// A bundle that does not pass verification must cost an unapplied update, never
// a dead engine.
func TestRejectedBundleLeavesTheWorkingOneInService(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "flowseal-1.0.0")
	mustMkdir(t, filepath.Join(old, "lists"))
	link := filepath.Join(base, "flowseal-current")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}

	i := NewFileInstaller(base)
	i.Verify = func(dir string) error { return errors.New("nfqws would not start") }
	err := i.Install(context.Background(), Release{Tag: "1.1.0"}, makeZip(t, map[string]string{"a.txt": "x", "b.txt": "y"}))
	if err == nil {
		t.Fatal("a rejected bundle was installed anyway")
	}
	if !strings.Contains(err.Error(), "staying on 1.0.0") {
		t.Errorf("the error does not say what is still in service: %v", err)
	}
	if target, _ := os.Readlink(link); target != old {
		t.Errorf("symlink moved to %s despite the rejection; want %s", target, old)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
