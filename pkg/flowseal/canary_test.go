package flowseal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const strategyScript = `#!/bin/sh
QNUM="${1:-200}"
N=/opt/zapret/binaries/linux-arm64/nfqws
L=/opt/flowseal-current/lists
B=/opt/flowseal-current/bin
exec "$N" --qnum="$QNUM" \
--filter-tcp=443 --hostlist="$L/list-general.txt" --dpi-desync=fake \
--dpi-desync-fake-tls="$B/tls_clienthello_www_google_com.bin" --new \
--filter-udp=443 --dpi-desync=fake --dpi-desync-fake-quic="$B/quic_initial.bin"
`

func canaryUnder(t *testing.T, run func(context.Context, string, []string) error) (*EngineCanary, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "active.sh")
	if err := os.WriteFile(script, []byte(strategyScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return &EngineCanary{
		Script:     script,
		CurrentDir: "/opt/flowseal-1.0.0",
		LinkPath:   "/opt/flowseal-current",
		QNum:       201,
		Run:        run,
	}, dir
}

// The point of the canary: a bundle the engine will not start on must not become
// the one in service.
func TestCanaryRejectsABundleTheEngineWillNotStartOn(t *testing.T) {
	var seen [][]string
	c, _ := canaryUnder(t, func(_ context.Context, _ string, argv []string) error {
		seen = append(seen, argv)
		if strings.Contains(strings.Join(argv, " "), "/opt/flowseal-2.0.0") {
			return errors.New("could not read tls_clienthello_www_google_com.bin")
		}
		return nil // the current bundle is fine
	})
	err := c.Verify("/opt/flowseal-2.0.0")
	if err == nil {
		t.Fatal("a bundle the engine refuses was accepted")
	}
	if !strings.Contains(err.Error(), "could not read") {
		t.Errorf("the rejection drops the engine's own words: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("ran %d times, want 2 (baseline then candidate)", len(seen))
	}
	// The baseline must test the CURRENT bundle, or it proves nothing.
	if base := strings.Join(seen[0], " "); !strings.Contains(base, "/opt/flowseal-1.0.0") || strings.Contains(base, "2.0.0") {
		t.Errorf("baseline did not run against the current bundle: %s", base)
	}
}

// The paths must actually move, including the stable symlink the script names —
// otherwise both runs test the same files and the canary always passes.
func TestCanaryRepointsEveryBundlePath(t *testing.T) {
	var candidate []string
	c, _ := canaryUnder(t, func(_ context.Context, _ string, argv []string) error {
		candidate = argv
		return nil
	})
	if err := c.Verify("/opt/flowseal-2.0.0"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(candidate, " ")
	if strings.Contains(got, "flowseal-current") || strings.Contains(got, "flowseal-1.0.0") {
		t.Errorf("a path still points at the old bundle: %s", got)
	}
	for _, want := range []string{
		"/opt/flowseal-2.0.0/lists/list-general.txt",
		"/opt/flowseal-2.0.0/bin/tls_clienthello_www_google_com.bin",
		"--new", // both blocks are tested, not just the first
	} {
		if !strings.Contains(got, want) {
			t.Errorf("candidate argv missing %q:\n%s", want, got)
		}
	}
	// The canary must never bind the queue that carries real traffic.
	if !strings.Contains(got, "--qnum=201") || strings.Contains(got, "--qnum=200") {
		t.Errorf("canary is not isolated onto its own queue: %s", got)
	}
}

// The engine binary lives outside the bundle and is what the canary must launch;
// Blocks throws it away with the invocation, so it has to be recovered.
func TestCanaryLaunchesTheProductionBinary(t *testing.T) {
	var prog string
	c, _ := canaryUnder(t, func(_ context.Context, p string, _ []string) error {
		prog = p
		return nil
	})
	if err := c.Verify("/opt/flowseal-2.0.0"); err != nil {
		t.Fatal(err)
	}
	if prog != "/opt/zapret/binaries/linux-arm64/nfqws" {
		t.Errorf("launched %q, want the engine the strategy script names", prog)
	}
}

// A canary that cannot run must stand aside rather than freeze updates forever.
func TestCanaryWithoutABaselineAcceptsTheBundle(t *testing.T) {
	c, _ := canaryUnder(t, func(_ context.Context, _ string, argv []string) error {
		return errors.New("nfqws: operation not permitted")
	})
	if err := c.Verify("/opt/flowseal-2.0.0"); err != nil {
		t.Errorf("no baseline should mean no verdict, got: %v", err)
	}
}

func TestCanaryWithoutAScriptAcceptsTheBundle(t *testing.T) {
	c, _ := canaryUnder(t, func(context.Context, string, []string) error { return nil })
	c.Script = filepath.Join(t.TempDir(), "does-not-exist.sh")
	if err := c.Verify("/opt/flowseal-2.0.0"); err != nil {
		t.Errorf("an unreadable strategy should not block an update, got: %v", err)
	}
}
