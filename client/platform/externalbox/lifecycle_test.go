package externalbox

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeBox points the Box at a stand-in binary that ignores its arguments and simply
// lives until it is signalled — enough to exercise the real spawn/reap/stop path
// without sing-box.
func fakeBox(t *testing.T) *Box {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals only")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "fake-singbox")
	// argv is "<stub> run -c <path>"; ignore it and sleep.
	if err := os.WriteFile(stub, []byte("#!"+sh+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(stub, filepath.Join(dir, "config.json"), slog.New(slog.DiscardHandler))
}

// What this test actually guarantees: stopping really ends the child, does not hang,
// and flips Alive(). That is worth having on its own — nothing covered the lifecycle
// before.
//
// What it does NOT do, stated plainly because a test that overclaims is worse than no
// test: it does not reproduce the race it was written for. Checked by restoring the old
// implementation — which read b.cmd from inside a goroutine while this function and the
// reaper both nilled it, and called Process.Wait() a second time against the reaper's —
// and -race stayed silent. The window needs the child to exit on its own at the same
// instant, which SIGTERM-then-immediate-death does not produce. The defects are real by
// inspection (an unguarded read of a mutex-guarded field; two waiters for one exit
// status, where the loser reports "waitid: no child processes" and replaces the log line
// that says why a crash loop is crashing) — they are simply not what this test proves.
func TestStopDoesNotRaceTheReaper(t *testing.T) {
	for i := 0; i < 5; i++ {
		b := fakeBox(t)
		b.mu.Lock()
		if err := b.spawnLocked(); err != nil {
			b.mu.Unlock()
			t.Fatalf("spawn: %v", err)
		}
		pid := b.cmd.Process.Pid
		b.mu.Unlock()

		if !b.Alive(t.Context()) {
			t.Fatal("a just-spawned box must report alive")
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			b.mu.Lock()
			b.stopLocked()
			b.mu.Unlock()
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("stop hung — a bounded wait must not block forever")
		}

		if b.Alive(t.Context()) {
			t.Error("a stopped box must not report alive")
		}
		// The child is really gone, not merely forgotten.
		if err := syscallKill0(pid); err == nil {
			t.Errorf("pid %d still exists after stop", pid)
		}
	}
}
