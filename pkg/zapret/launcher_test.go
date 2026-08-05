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
