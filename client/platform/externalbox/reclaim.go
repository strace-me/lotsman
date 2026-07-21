package externalbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ReclaimStale kills a sing-box left behind by a previous run of this client.
//
// A client killed with SIGKILL never gets to stop its child, so the tunnel stays
// up unsupervised: traffic still flows, but nothing is steering it and the next
// start cannot bind the same ports. Recording the pid and reclaiming it on
// startup is deterministic, unlike asking the kernel to signal us on parent death
// — Go's Pdeathsig fires when the spawning THREAD exits, which the runtime may do
// while the process is perfectly healthy, and a misfire would tear down a working
// tunnel. Better to clean up late than to risk killing a live one early.
//
// The recorded pid is only trusted when the process it names is still the one we
// started, checked against its command line, because pids are reused.
func ReclaimStale(pidFile, configPath string) (reclaimed int, err error) {
	raw, err := os.ReadFile(pidFile)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("externalbox: read pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		os.Remove(pidFile)
		return 0, nil
	}
	if !isOurSingbox(pid, configPath) {
		os.Remove(pidFile) // stale record, or the pid now belongs to somebody else
		return 0, nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return 0, fmt.Errorf("externalbox: reclaim pid %d: %w", pid, err)
	}
	os.Remove(pidFile)
	return pid, nil
}

// isOurSingbox reports whether pid is a live sing-box running OUR config.
func isOurSingbox(pid int, configPath string) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false // not Linux, or the process is gone
	}
	cmd := strings.ReplaceAll(string(raw), "\x00", " ")
	return strings.Contains(cmd, "sing-box") && strings.Contains(cmd, configPath)
}

// recordPid notes the running child so a later run can reclaim it.
func recordPid(pidFile string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o644)
}
