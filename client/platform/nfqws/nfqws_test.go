package nfqws

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	return &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// An engine that dies on its own is the desync rung's blind spot: the nft rules
// carry `flags bypass`, so traffic keeps flowing undesynced and nothing downstream
// looks wrong. The record is the only thing that later says why.
func TestRecordCrashWritesWhatTheEngineSaid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nfqws-crash.log")
	e := testEngine(t)
	e.SetCrashLog(path)

	e.recordCrash([]string{"--qnum=200", "--dpi-desync=fake"}, errors.New("exit status 1"),
		"could not read tls_clienthello_4pda_to.bin\n")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("crash log not written: %v", err)
	}
	for _, want := range []string{"exit status 1", "--dpi-desync=fake", "tls_clienthello_4pda_to.bin"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("crash record is missing %q:\n%s", want, body)
		}
	}

	// Successive deaths must accumulate: a restart loop is only diagnosable as a
	// sequence, and truncating would leave just the last identical-looking failure.
	e.recordCrash([]string{"--qnum=200"}, errors.New("exit status 1"), "again\n")
	body, _ = os.ReadFile(path)
	if got := strings.Count(string(body), "nfqws exited"); got != 2 {
		t.Errorf("records after two crashes = %d, want 2", got)
	}
}

// No path configured must stay silent rather than erroring or writing somewhere
// of its own choosing.
func TestRecordCrashWithoutPathIsANoop(t *testing.T) {
	dir := t.TempDir()
	e := testEngine(t)
	e.recordCrash([]string{"--qnum=200"}, errors.New("boom"), "said something")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d files with no crash log configured", len(entries))
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	var b tailBuffer
	b.Write([]byte(strings.Repeat("x", 5000)))
	b.Write([]byte("the last words"))
	got := b.String()
	if len(got) > 4096 {
		t.Errorf("tail buffer grew to %d bytes, want <= 4096", len(got))
	}
	if !strings.HasSuffix(got, "the last words") {
		t.Errorf("tail buffer dropped the end: %q", got[max(0, len(got)-40):])
	}
}

func TestParseDefaultRouteIface(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "ethernet",
			out:  "default via 192.168.1.1 dev eth0 proto dhcp src 192.168.1.50 metric 100\n",
			want: "eth0",
		},
		{
			name: "wifi with trailing newline",
			out:  "default via 10.0.0.1 dev wlan0 proto dhcp metric 600\n\n",
			want: "wlan0",
		},
		{
			// A laptop often has both; the first (lowest metric) line wins, which is
			// the route packets actually take.
			name: "multiple defaults picks the first",
			out: "default via 10.0.0.1 dev wlan0 proto dhcp metric 600\n" +
				"default via 192.168.1.1 dev eth0 proto dhcp metric 100\n",
			want: "wlan0",
		},
		{
			name: "no default route",
			out:  "10.0.0.0/24 dev wlan0 proto kernel scope link src 10.0.0.5\n",
			want: "",
		},
		{name: "empty", out: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseDefaultRouteIface(tc.out); got != tc.want {
				t.Errorf("ParseDefaultRouteIface() = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeEngine writes a stand-in for nfqws: it refuses when its arguments carry
// REFUSE, and otherwise lives long enough to look healthy.
func fakeEngine(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-engine.sh")
	body := "#!/bin/sh\ncase \"$*\" in *REFUSE*) echo 'could not read tls_clienthello_x.bin' >&2; exit 1;; esac\nsleep 5\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func engineFor(t *testing.T) *Engine {
	t.Helper()
	e := &Engine{bin: fakeEngine(t), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.inst.QNum = 200
	// Long enough that a refusing process is certainly observed: at the
	// production window this test races the fork on a loaded machine and reads a
	// refusal as a success — which is the exact defect it exists to catch.
	e.settle = 3 * time.Second
	t.Cleanup(func() { e.mu.Lock(); e.stopProcessLocked(); e.mu.Unlock() })
	return e
}

// The load-bearing property behind rollback: a launch that failed must leave the
// recorded identity alone. If a failed attempt overwrote lastArgs, the rollback
// would faithfully restore the strategy that had just been rejected.
func TestFailedLaunchDoesNotClaimTheEngine(t *testing.T) {
	e := engineFor(t)
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.launchLocked([]string{"--dpi-desync=fake", "--good"}); err != nil {
		t.Fatalf("healthy engine reported as failed: %v", err)
	}
	if e.cmd == nil || len(e.lastArgs) != 2 {
		t.Fatalf("a successful launch did not record itself: cmd=%v args=%v", e.cmd != nil, e.lastArgs)
	}
	good := slices.Clone(e.lastArgs)
	live := e.cmd

	err := e.launchLocked([]string{"--dpi-desync=REFUSE"})
	if err == nil {
		t.Fatal("an engine that exits immediately was reported as started")
	}
	// The engine's own words, not just an exit status.
	if !strings.Contains(err.Error(), "tls_clienthello_x.bin") {
		t.Errorf("the failure dropped the engine's explanation: %v", err)
	}
	if !slices.Equal(e.lastArgs, good) {
		t.Errorf("a failed launch overwrote the recorded strategy: %v, want %v", e.lastArgs, good)
	}
	if e.cmd != live {
		t.Error("a failed launch replaced the recorded process handle")
	}
}
