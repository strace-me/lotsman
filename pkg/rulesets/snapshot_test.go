package rulesets

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func relFor(tag string) (string, bool) { return "rule-set-geosite/" + tag + ".srs", true }

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestSnapshotRestorePutsBothKindsOfTagBack(t *testing.T) {
	base := t.TempDir()
	live, snaps := filepath.Join(base, "live"), filepath.Join(base, "snaps")
	write(t, filepath.Join(live, "rule-set-geosite/geosite-youtube.srs"), "OLD")

	// geosite-discord does not exist live: the release ADDS it.
	snap, err := Snapshot(live, []string{"geosite-youtube", "geosite-discord"}, snaps, relFor, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(live, "rule-set-geosite/geosite-youtube.srs"), "NEW")
	write(t, filepath.Join(live, "rule-set-geosite/geosite-discord.srs"), "ADDED")

	if err := Restore(snap, live, relFor); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(live, "rule-set-geosite/geosite-youtube.srs")); got != "OLD" {
		t.Errorf("changed tag not restored: %q", got)
	}
	// A tag the release added must be REMOVED, not left behind: keeping it would
	// leave a live set that is neither release.
	if _, err := os.Stat(filepath.Join(live, "rule-set-geosite/geosite-discord.srs")); !os.IsNotExist(err) {
		t.Error("a tag added by the rejected release survived the rollback")
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"20260101-000000", "20260102-000000", "20260103-000000", "20260104-000000"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneSnapshots(dir, 2); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 2 || left[0].Name() != "20260103-000000" {
		t.Errorf("prune kept %v, want the two newest", names(left))
	}
}

func names(es []os.DirEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

type stubReleaser struct{ dir string }

func (s stubReleaser) Latest(context.Context, string) (string, error) { return "v2", nil }
func (s stubReleaser) Fetch(context.Context, string, string) (string, func(), error) {
	return s.dir, func() {}, nil
}

type stubCounter struct{}

func (stubCounter) Count(p string) (int, error) {
	if _, err := os.Stat(p); err != nil {
		return 0, err
	}
	return 100, nil
}

// The failure the design named and the code never had: the reconcile runs AFTER
// the files have moved, so a release whose rule-sets make `sing-box check` fail
// would otherwise stay in service with nothing to undo it.
func TestReconcileRejectionRollsTheRuleSetsBack(t *testing.T) {
	base := t.TempDir()
	live, src, snaps := filepath.Join(base, "live"), filepath.Join(base, "src"), filepath.Join(base, "snaps")
	write(t, filepath.Join(live, "rule-set-geosite/geosite-youtube.srs"), "OLD")
	write(t, filepath.Join(src, "rule-set-geosite/geosite-youtube.srs"), "NEW")

	u := &Updater{
		Repo: "x/y", Required: []string{"geosite-youtube"}, LiveDir: live,
		MinRatio: 0.7, PerTag: true, Counter: stubCounter{}, Releaser: stubReleaser{dir: src},
		PathFor: relFor, SnapshotDir: snaps, Now: func() time.Time { return time.Unix(0, 0) },
		OnApplied: func(context.Context) error { return errors.New("sing-box check: invalid rule-set") },
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	err := u.Update(context.Background())
	if err == nil {
		t.Fatal("a release the reconcile rejected was reported as applied")
	}
	if got := read(t, filepath.Join(live, "rule-set-geosite/geosite-youtube.srs")); got != "OLD" {
		t.Errorf("live rule-set is %q, want the pre-update content back", got)
	}
}
