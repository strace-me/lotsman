package flowseal

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func TestUnzipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	raw := makeZip(t, map[string]string{"../escape.txt": "evil"})
	if err := unzip(raw, dir); err == nil {
		t.Error("expected zip-slip rejection")
	}
}
