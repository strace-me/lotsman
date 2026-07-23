package externalbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// started, checked against its command line, because pids are reused. Identity
// verification is platform-specific (see reclaim_linux.go / reclaim_other.go);
// where it cannot be done, ReclaimStale declines to signal rather than guess.
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
	ours, resolved := isOurSingbox(pid, configPath)
	if !resolved {
		// Could not read the process at all (a transient error, not "it is gone").
		// Deleting the record now would drop our only handle on a live orphan, and
		// signalling a pid we cannot identify could hit an unrelated process that
		// reused the number. Leave the record and try again next start.
		return 0, nil
	}
	if !ours {
		os.Remove(pidFile) // stale record, or the pid now belongs to somebody else
		return 0, nil
	}
	if err := terminate(pid); err != nil {
		return 0, fmt.Errorf("externalbox: reclaim pid %d: %w", pid, err)
	}
	os.Remove(pidFile)
	return pid, nil
}

// recordPid notes the running child so a later run can reclaim it.
func recordPid(pidFile string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o644)
}
