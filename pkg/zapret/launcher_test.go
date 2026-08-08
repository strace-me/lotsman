package zapret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBin(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "engine.sh")
	body := "#!/bin/sh\ncase \"$*\" in *REFUSE*) echo 'could not read x.bin' >&2; exit 1;; esac\nsleep 5\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// An engine that dies on its inputs must not be reported as started: the tuner
// would then measure through nothing and score every candidate identically.
func TestLauncherWaitsForTheEngineToSurvive(t *testing.T) {
	// Long enough that a refusing process is certainly observed. At a short window
	// this races the fork on a loaded machine and reads a refusal as a success —
	// the exact defect the test exists to catch.
	l := ExecLauncher(3 * time.Second)
	bin := fakeBin(t)

	_, err := l(context.Background(), bin, "", []string{"--qnum=201", "--REFUSE"})
	if err == nil {
		t.Fatal("an engine that exited immediately was reported as started")
	}
	if !strings.Contains(err.Error(), "could not read x.bin") {
		t.Errorf("the failure dropped the engine's own words: %v", err)
	}

	stop, err := l(context.Background(), bin, "", []string{"--qnum=201", "--dpi-desync=fake"})
	if err != nil {
		t.Fatalf("a healthy engine was reported as failed: %v", err)
	}
	stop()
}

// An engine that starts, complains and keeps running is the case between the two
// the launcher already handled, and it is the one that produces a measurement of
// a strategy the binary never applied. Its words were captured and thrown away
// unless it died.
func TestASurvivingEngineIsStillHeard(t *testing.T) {
	var heard string
	l := ExecLauncherSaying(80*time.Millisecond, func(said string) { heard = said })
	stop, err := l(context.Background(), "/bin/sh",
		"", []string{"-c", "echo 'unknown option --dpi-desync-nonsense, ignored' >&2; sleep 5"})
	if err != nil {
		t.Fatalf("the engine survived; the launcher must report a start: %v", err)
	}
	defer stop()
	if !strings.Contains(heard, "unknown option") {
		t.Errorf("the engine's complaint was dropped, got %q", heard)
	}
}

// And a quiet engine stays quiet — a sink that fires on every start would train
// the reader to skip the line that matters.
func TestASilentEngineSaysNothing(t *testing.T) {
	fired := false
	l := ExecLauncherSaying(80*time.Millisecond, func(string) { fired = true })
	stop, err := l(context.Background(), "/bin/sh", "", []string{"-c", "sleep 5"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if fired {
		t.Error("a silent start produced a warning")
	}
}
