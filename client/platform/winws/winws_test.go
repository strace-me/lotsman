package winws

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/zapret"
)

func TestCaptureArgsRendersTheFilterTheBundlesUse(t *testing.T) {
	// The spellings and the range form are taken from a real Flowseal bundle
	// (pkg/strategyimport/testdata/flowseal-1.10.0-alt12.bat), not from memory.
	got, err := CaptureArgs(zapret.Capture{
		TCP: []string{"80", "443", "2053", "8443"},
		UDP: []string{"443", "19294-19344", "50000-50100"},
	})
	if err != nil {
		t.Fatalf("capture args: %v", err)
	}
	want := []string{
		"--wf-tcp=80,443,2053,8443",
		"--wf-udp=443,19294-19344,50000-50100",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("capture args =\n  %v\nwant\n  %v", got, want)
	}
}

func TestCaptureArgsOmitsAProtocolWithNoPorts(t *testing.T) {
	got, err := CaptureArgs(zapret.Capture{TCP: []string{"443"}})
	if err != nil {
		t.Fatalf("capture args: %v", err)
	}
	if len(got) != 1 || got[0] != "--wf-tcp=443" {
		t.Errorf("got %v, want just the tcp filter", got)
	}
}

// An empty capture must be refused, not rendered as an empty filter: winws would
// start, look healthy, and desync nothing.
func TestCaptureArgsRefusesAnEmptySpec(t *testing.T) {
	if _, err := CaptureArgs(zapret.Capture{}); err == nil {
		t.Fatal("expected an error for a capture that names no ports")
	}
}

func TestCaptureArgsRefusesJunkPorts(t *testing.T) {
	for _, bad := range []string{"http", "80;rm -rf /", "-1", "80-", "1234567"} {
		if _, err := CaptureArgs(zapret.Capture{TCP: []string{bad}}); err == nil {
			t.Errorf("port %q was accepted", bad)
		}
	}
}

// Settle windows for the two kinds of test, and the reason they differ.
//
// The window is a real-time measurement — "did it survive long enough to count as
// started" — so a test that picks one number for both cases races the process
// scheduler and loses under load, which is exactly what the first version of this
// file did (green alone, red under `go test ./...`).
//
// A REFUSAL test wants a LONG window and pays nothing for it: the select returns
// the moment the stub exits, so the window is only an upper bound on how long we
// are willing to wait for a refusal to show up. A SUCCESS test wants a SHORT one,
// because it waits the whole window out — and shortness cannot make it wrong,
// since a process that started is alive whether or not the shell has got as far
// as its first line.
const (
	settleRefusal = 5 * time.Second
	settleSuccess = 100 * time.Millisecond
)

// stubEngine builds an engine whose "winws" is a script: it stays up unless the
// argv contains REFUSE, in which case it complains and exits — the shape of a
// real refusal, which winws reports after start rather than by failing to start.
func stubEngine(t *testing.T, settle time.Duration) (*Engine, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "winws-stub")
	body := "#!/bin/sh\ncase \"$*\" in *REFUSE*) echo 'windivert: driver not loaded' >&2; exit 1;; esac\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	e := New(bin, zapret.Instance{Capture: zapret.Capture{TCP: []string{"443"}}}, dir,
		slog.New(slog.DiscardHandler))
	e.SetSettle(settle)
	t.Cleanup(func() { e.Stop(context.Background()) })
	return e, dir
}

// waitForFile polls until path exists, so a test never assumes the child got as
// far as writing it within some arbitrary window.
func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine never wrote %s — it did not run the stub", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestApplyPrependsTheCaptureFilter(t *testing.T) {
	e, dir := stubEngine(t, settleSuccess)
	// The stub records what it was called with, so the assertion is on the argv
	// that actually reached the process rather than on what we meant to send.
	bin := filepath.Join(dir, "winws-stub")
	body := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "argv") + "\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("rewrite stub: %v", err)
	}
	if _, err := e.Apply(context.Background(), []string{"--dpi-desync=fake", "--dpi-desync-repeats=8"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := strings.TrimSpace(string(waitForFile(t, filepath.Join(dir, "argv"))))
	want := "--wf-tcp=443 --dpi-desync=fake --dpi-desync-repeats=8"
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestReapplyingTheSameStrategyDoesNotRestart(t *testing.T) {
	e, _ := stubEngine(t, settleSuccess)
	args := []string{"--dpi-desync=fake"}
	restarted, err := e.Apply(context.Background(), args)
	if err != nil || !restarted {
		t.Fatalf("first apply: restarted=%v err=%v", restarted, err)
	}
	restarted, err = e.Apply(context.Background(), args)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if restarted {
		t.Error("re-applying the same strategy restarted the engine — that drops the desync mid-flow")
	}
}

// The keep-last-good rule: a strategy the engine refuses must leave the previous
// one running, and the caller must be told which of the two it is looking at.
func TestARefusedStrategyKeepsThePreviousOne(t *testing.T) {
	// The first launch succeeds, so it waits its window out — keep that one short.
	// The window that has to be long is the refusing call's.
	e, _ := stubEngine(t, settleSuccess)
	good := []string{"--dpi-desync=fake", "--good"}
	if _, err := e.Apply(context.Background(), good); err != nil {
		t.Fatalf("apply good: %v", err)
	}
	e.SetSettle(settleRefusal)
	_, err := e.Apply(context.Background(), []string{"--dpi-desync=fake", "--REFUSE"})
	if err == nil {
		t.Fatal("a refusing strategy was reported as applied")
	}
	if !strings.Contains(err.Error(), "kept the previous strategy") {
		t.Errorf("error does not say what is running now: %v", err)
	}
	if !e.Alive() {
		t.Error("the previous strategy was not restored — the desync is down")
	}
}

func TestAFailedFirstLaunchDoesNotClaimTheEngine(t *testing.T) {
	e, _ := stubEngine(t, settleRefusal)
	if _, err := e.Apply(context.Background(), []string{"--REFUSE"}); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	if e.Alive() {
		t.Error("Alive() reports an engine that never started")
	}
}

func TestApplyRefusesAnEmptyStrategy(t *testing.T) {
	e, _ := stubEngine(t, settleSuccess)
	if _, err := e.Apply(context.Background(), nil); err == nil {
		t.Fatal("an empty strategy was accepted")
	}
}
