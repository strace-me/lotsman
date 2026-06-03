package blockcheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCmd returns canned output and records the env it was given.
type fakeCmd struct {
	out    []byte
	err    error
	gotEnv []string
}

func (f *fakeCmd) Output(_ context.Context, env []string, _ string, _ ...string) ([]byte, error) {
	f.gotEnv = env
	return f.out, f.err
}

func TestRunParsesAndBuildsEnv(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sim_quick_rutracker.txt"))
	if err != nil {
		t.Skipf("no fixture: %v", err)
	}
	fc := &fakeCmd{out: data}
	r := New(fc)

	got, err := r.Run(context.Background(), Options{
		ScriptPath: "/opt/zapret/blockcheck.sh",
		ZapretBase: "/opt/zapret",
		Platform:   "linux-arm64",
		Domains:    []string{"rutracker.org"},
		ScanLevel:  "quick",
		IPV:        4,
		Simulate:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no strategies parsed")
	}

	env := strings.Join(fc.gotEnv, "\n")
	for _, want := range []string{"BATCH=1", "SCANLEVEL=quick", "IPV=4", "DOMAINS=rutracker.org", "SIMULATE=1", "NFQWS=/opt/zapret/binaries/linux-arm64/nfqws"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q; got:\n%s", want, env)
		}
	}
}

func TestRunValidates(t *testing.T) {
	r := New(&fakeCmd{})
	if _, err := r.Run(context.Background(), Options{Domains: []string{"x"}}); err == nil {
		t.Error("expected error for empty script path")
	}
	if _, err := r.Run(context.Background(), Options{ScriptPath: "bc.sh"}); err == nil {
		t.Error("expected error for no domains")
	}
}

func TestRunReturnsParsedOnNonZeroExit(t *testing.T) {
	// blockcheck can exit non-zero but still print a usable SUMMARY.
	fc := &fakeCmd{out: []byte(sample), err: errors.New("exit 1")}
	r := New(fc)
	got, err := r.Run(context.Background(), Options{ScriptPath: "bc.sh", Domains: []string{"rutracker.org"}})
	if err == nil {
		t.Error("expected error surfaced")
	}
	if len(got) == 0 {
		t.Error("expected parsed results despite non-zero exit")
	}
}

func TestBuildEnvDefaults(t *testing.T) {
	env := strings.Join(buildEnv(Options{Domains: []string{"a", "b"}}), "\n")
	for _, want := range []string{"SCANLEVEL=standard", "IPV=4", "DOMAINS=a b"} {
		if !strings.Contains(env, want) {
			t.Errorf("defaults missing %q; got:\n%s", want, env)
		}
	}
	if strings.Contains(env, "SIMULATE=1") {
		t.Error("SIMULATE must be off by default")
	}
	if strings.Contains(env, "NFQWS=") {
		t.Error("no binary overrides expected without ZapretBase/Platform")
	}
}
