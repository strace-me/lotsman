package rulesets

import (
	"archive/zip"
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A file that EXISTS is not the same as a file that is USABLE, and the difference is
// the whole point: an extract cut short by a reboot left a truncated .srs, sing-box
// loaded it and matched nothing, and because the name was present "already
// provisioned" skipped it on every boot after. A permanent silent outage, caused by
// the check that was supposed to be the safe answer.
// bodyCounter stands in for sing-box: a rule-set is readable when its body is the
// complete one, which is exactly the property the truncated-file case breaks.
type bodyCounter struct{ complete string }

func (c bodyCounter) Count(p string) (int, error) {
	got, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	if string(got) != c.complete {
		return 0, os.ErrInvalid
	}
	return len(got), nil
}

func bundle(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAReadableRuleSetIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "rule-set-geosite", "geosite-youtube.srs")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := bundle(t, map[string]string{"rule-set-geosite/geosite-youtube.srs": "complete"})

	err := EnsureFromCounter(context.Background(), Source{URL: srv.URL}, dir,
		[]string{"geosite-youtube"}, bodyCounter{complete: "complete"},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// No download should have happened for a file that is already fine; the server
	// would have replaced it with the same bytes, so assert on the snapshot instead.
	if _, err := os.Stat(filepath.Join(dir, "snapshots")); err == nil {
		t.Error("a snapshot was taken with nothing to overwrite")
	}
}

func TestATruncatedRuleSetIsRepaired(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "rule-set-geosite", "geosite-youtube.srs")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("trunc"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := bundle(t, map[string]string{"rule-set-geosite/geosite-youtube.srs": "the whole thing"})

	err := EnsureFromCounter(context.Background(), Source{URL: srv.URL}, dir,
		[]string{"geosite-youtube"}, bodyCounter{complete: "the whole thing"},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("a truncated rule-set was not repaired: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the whole thing" {
		t.Errorf("the file was not replaced: %q", got)
	}
	// A snapshot must exist, or a failed repair would have nothing to fall back to.
	if _, err := os.Stat(filepath.Join(dir, "snapshots")); err != nil {
		t.Error("no snapshot was taken before overwriting a rule-set")
	}
}

func TestAFailedRepairLeavesTheOldFileAlone(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "rule-set-geosite", "geosite-youtube.srs")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("trunc"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A bundle that does NOT carry the file: the repair cannot complete.
	srv := bundle(t, map[string]string{"rule-set-geoip/geoip-telegram.srs": "unrelated"})

	err := EnsureFromCounter(context.Background(), Source{URL: srv.URL}, dir,
		[]string{"geosite-youtube"}, bodyCounter{complete: "the whole thing"},
		slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("a repair that could not complete reported success")
	}
	// The snapshot held the previous bytes, so the restore must bring back something
	// the operator can still use rather than leaving the truncated file in place.
	if _, serr := os.Stat(dst); serr != nil {
		t.Error("the destination was removed rather than restored")
	}
}
