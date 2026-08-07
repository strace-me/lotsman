package aggregate

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// The operator picks `out:`, and nothing guarantees its parent exists. Measured
// the day packs arrived: both lists fetched and merged correctly and the write
// failed every cycle, because the atomic swap writes a temp file NEXT TO the
// target and the directory one level down had never been created. The error even
// named a path the operator never typed — the temp file.
func TestRebuildCreatesTheOutputDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "packs", "nested", "flowseal-google.txt")

	spec := RebuildSpec{Name: "flowseal-google", Out: out, Domains: []string{"youtube.com"}}
	if _, err := Rebuild(context.Background(), NewManager(nil), spec, false, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("rebuild into a missing directory: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("hostlist not written: %v", err)
	}
}
