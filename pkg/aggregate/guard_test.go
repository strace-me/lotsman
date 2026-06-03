package aggregate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShrinkOK(t *testing.T) {
	cases := []struct {
		name       string
		prev, next int
		ratio      float64
		want       bool
	}{
		{"first run passes", 0, 5, 0.7, true},
		{"guard disabled passes", 100, 1, 0, true},
		{"growth passes", 100, 200, 0.7, true},
		{"small shrink within ratio passes", 100, 80, 0.7, true},
		{"exactly at ratio passes", 100, 70, 0.7, true},
		{"shrink below ratio fails", 100, 50, 0.7, false},
		{"collapse to near-zero fails", 1000, 3, 0.7, false},
	}
	for _, c := range cases {
		if got := ShrinkOK(c.prev, c.next, c.ratio); got != c.want {
			t.Errorf("%s: ShrinkOK(%d,%d,%v)=%v want %v", c.name, c.prev, c.next, c.ratio, got, c.want)
		}
	}
}

func TestShrinkByKey(t *testing.T) {
	prev := map[string]int{
		"geosite-youtube":    1000,
		"geosite-discord":    200,
		"geosite-shrinkable": 100, // a tag that legitimately shrank a little
		"geosite-gone":       50,  // a tag that vanishes in the new release
	}
	next := map[string]int{
		"geosite-youtube":    1100, // grew
		"geosite-discord":    100,  // halved -> below 0.7 -> fail
		"geosite-shrinkable": 80,   // -20% -> within 0.7 -> pass
		"geosite-new":        10,   // first-seen -> pass
		// geosite-gone absent -> next=0 -> fail
	}
	failed := ShrinkByKey(prev, next, 0.7)

	if _, ok := failed["geosite-discord"]; !ok {
		t.Error("geosite-discord halved, should fail per-tag guard")
	}
	if f, ok := failed["geosite-gone"]; !ok || f.Next != 0 {
		t.Errorf("vanished tag should fail with next=0, got %+v ok=%v", f, ok)
	}
	for _, ok := range []string{"geosite-youtube", "geosite-shrinkable", "geosite-new"} {
		if _, bad := failed[ok]; bad {
			t.Errorf("%s should pass per-tag guard", ok)
		}
	}
	if len(failed) != 2 {
		t.Errorf("failed = %v, want exactly discord + gone", failed)
	}
}

func TestShrinkByKeyAllPass(t *testing.T) {
	prev := map[string]int{"a": 10, "b": 20}
	next := map[string]int{"a": 10, "b": 25}
	if failed := ShrinkByKey(prev, next, 0.7); failed != nil {
		t.Errorf("expected nil (all pass), got %v", failed)
	}
}

func TestWriteIfChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.txt")

	// First write: file missing -> created, changed=true.
	changed, err := WriteIfChanged(path, []byte("a\nb\n"), 0o644)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v, want true/nil", changed, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a\nb\n" {
		t.Fatalf("content = %q", got)
	}

	// Identical content -> no write, changed=false.
	changed, err = WriteIfChanged(path, []byte("a\nb\n"), 0o644)
	if err != nil || changed {
		t.Fatalf("identical write: changed=%v err=%v, want false/nil (eMMC no-op)", changed, err)
	}

	// Different content -> rewritten, changed=true.
	changed, err = WriteIfChanged(path, []byte("a\nb\nc\n"), 0o644)
	if err != nil || !changed {
		t.Fatalf("changed write: changed=%v err=%v, want true/nil", changed, err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "a\nb\nc\n" {
		t.Fatalf("content = %q", got)
	}

	// No leftover temp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file should not linger")
	}
}
