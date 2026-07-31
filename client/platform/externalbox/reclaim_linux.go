// NOTE: plain "linux" also matches ANDROID, where there is no external sing-box
// process to reclaim (libbox runs in-process) — desktop/router Linux only.
//go:build linux && !android

package externalbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// isOurSingbox reports whether pid is a live sing-box running OUR config. The
// second return says whether the answer is trustworthy: a process that is gone
// resolves cleanly to "not ours", but a /proc entry we could not read at all is
// unresolved, and the caller must not act on an unresolved verdict.
func isOurSingbox(pid int, configPath string) (ours, resolved bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if os.IsNotExist(err) {
		return false, true // the process is gone — a genuinely stale record
	}
	if err != nil {
		return false, false // could not read it (permission/transient) — do not assume
	}
	cmd := strings.ReplaceAll(string(raw), "\x00", " ")
	return strings.Contains(cmd, "sing-box") && strings.Contains(cmd, configPath), true
}

// terminate asks pid to shut down cleanly so sing-box removes its auto_route
// rules and tun on the way out.
func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
