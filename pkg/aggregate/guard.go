package aggregate

import (
	"bytes"
	"os"
	"path/filepath"
)

// ShrinkOK reports whether a refreshed entry count is acceptable against the
// last good count under minRatio (e.g. 0.7 = reject if the new list dropped
// below 70% of the old). This is the GLOBAL shrink guard — the only mode
// meaningful for a single merged list — protecting against an upstream that
// half-breaks and silently halves coverage.
//
// A non-positive prev (first run / unknown) always passes; a non-positive
// minRatio disables the guard. Growth always passes.
func ShrinkOK(prev, next int, minRatio float64) bool {
	if prev <= 0 || minRatio <= 0 {
		return true
	}
	return float64(next) >= float64(prev)*minRatio
}

// ShrinkFail records a key that failed the per-key shrink guard.
type ShrinkFail struct {
	Prev, Next int
}

// ShrinkByKey applies the shrink guard independently to each key (e.g. one
// rule-set tag in a release) and returns the keys whose new count dropped below
// minRatio of their last good count. This is the PER-TAG shrink guard for
// rule-set releases: a single legitimately-shrinking tag is reported on its own
// and need not block the rest of the release (the caller decides), unlike a
// global check that would reject the whole download.
//
// Semantics: a key absent from prev is first-seen and passes. A key present in
// prev but absent from next counts as next=0 — a tag that vanished entirely is a
// regression and fails. An empty result means every tag passed.
func ShrinkByKey(prev, next map[string]int, minRatio float64) map[string]ShrinkFail {
	var failed map[string]ShrinkFail
	for key, p := range prev {
		n := next[key] // absent => 0 => the tag vanished
		if !ShrinkOK(p, n, minRatio) {
			if failed == nil {
				failed = make(map[string]ShrinkFail)
			}
			failed[key] = ShrinkFail{Prev: p, Next: n}
		}
	}
	return failed
}

// WriteIfChanged writes body to path atomically (temp + rename) only when the
// current file contents differ. It returns changed=false with no write when the
// content is byte-identical — the no-op that saves eMMC wear, since most
// autoupdate refreshes produce the same list. A missing/unreadable target is
// treated as "differs" so the file is created.
func WriteIfChanged(path string, body []byte, perm os.FileMode) (changed bool, err error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, body) {
		return false, nil
	}
	// A UNIQUE temp name, not path+".tmp": the desktop client and the router daemon
	// can both rebuild the same list on one machine, and a shared fixed name lets one
	// writer truncate the other's partial file so the loser renames a spliced list into
	// place. The rename itself stays atomic, so a reader never sees a torn file.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}
	// CreateTemp makes the file 0600; the callers' lists must stay readable by nfqws
	// after it drops privileges.
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}
