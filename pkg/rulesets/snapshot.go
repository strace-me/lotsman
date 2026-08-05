package rulesets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// manifestName lists every tag a snapshot covers and whether it existed live at
// the time. A tag the release ADDS has no previous file, so restoring it means
// deleting the new one — without the manifest a rollback would leave the added
// tag behind and the live set would be neither the old release nor the new.
const manifestName = "manifest"

// Snapshot copies the live .srs for tags into a new timestamped directory under
// snapDir and returns its path.
//
// This is the piece docs/AUTOUPDATE.md has specified since June — "atomic swap,
// keep the prior snapshot for rollback" — and never had. Its absence leaves two
// live hazards, not one. A swap that fails part-way through leaves some tags on
// the new release and some on the old, a state no release ever produced. And the
// post-apply reconcile runs AFTER the files have moved, so a release whose
// rule-sets make `sing-box check` fail stays in service with nothing to undo it.
func Snapshot(liveDir string, tags []string, snapDir string, pathFor PathFor, now time.Time) (string, error) {
	dir := filepath.Join(snapDir, now.UTC().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("rulesets: snapshot dir: %w", err)
	}
	var manifest []string
	for _, tag := range tags {
		rel, ok := pathFor(tag)
		if !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(liveDir, rel))
		if err != nil {
			if os.IsNotExist(err) {
				manifest = append(manifest, "new\t"+tag)
				continue
			}
			return "", fmt.Errorf("rulesets: snapshot %q: %w", tag, err)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return "", fmt.Errorf("rulesets: snapshot %q: %w", tag, err)
		}
		manifest = append(manifest, "had\t"+tag)
	}
	sort.Strings(manifest)
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(strings.Join(manifest, "\n")+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("rulesets: snapshot manifest: %w", err)
	}
	return dir, nil
}

// Restore puts the live directory back to the state a snapshot recorded: tags
// that had a file get it back, tags that did not are removed. Best-effort across
// tags — one unrestorable tag must not abandon the rest half-way, which is the
// very state a rollback exists to prevent — and it reports every failure.
func Restore(snap, liveDir string, pathFor PathFor) error {
	body, err := os.ReadFile(filepath.Join(snap, manifestName))
	if err != nil {
		return fmt.Errorf("rulesets: read snapshot manifest: %w", err)
	}
	var failures []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		state, tag, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		rel, mapped := pathFor(tag)
		if !mapped {
			continue
		}
		live := filepath.Join(liveDir, rel)
		if state == "new" {
			if err := os.Remove(live); err != nil && !os.IsNotExist(err) {
				failures = append(failures, tag+": "+err.Error())
			}
			continue
		}
		prev, err := os.ReadFile(filepath.Join(snap, rel))
		if err != nil {
			failures = append(failures, tag+": "+err.Error())
			continue
		}
		if err := os.WriteFile(live, prev, 0o644); err != nil {
			failures = append(failures, tag+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("rulesets: restore incomplete: %s", strings.Join(failures, "; "))
	}
	return nil
}

// PruneSnapshots keeps the newest keep snapshots and removes the rest. The
// directory names sort chronologically, so lexical order is chronological order.
// eMMC hygiene: the box's flash is the reason the design capped this rather than
// keeping every snapshot forever.
func PruneSnapshots(snapDir string, keep int) error {
	if keep < 1 {
		return nil
	}
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return nil
	}
	sort.Strings(dirs)
	for _, old := range dirs[:len(dirs)-keep] {
		if err := os.RemoveAll(filepath.Join(snapDir, old)); err != nil {
			return err
		}
	}
	return nil
}
