//go:build !linux

package externalbox

import "errors"

// isOurSingbox cannot verify process identity without /proc, so it never claims a
// pid as ours. It reports resolved=true so ReclaimStale discards the stale record
// (matching the Linux "process gone" path) but, with ours=false, never signals:
// the desktop client only owns a tun/desync on Linux, and on other platforms a
// kill of an unverifiable, possibly-reused pid is not worth the risk.
func isOurSingbox(_ int, _ string) (ours, resolved bool) {
	return false, true
}

// terminate is unreachable on non-Linux (isOurSingbox always returns ours=false,
// so ReclaimStale short-circuits before calling it) but must exist to build.
func terminate(_ int) error {
	return errors.New("externalbox: process reclaim is Linux-only")
}
