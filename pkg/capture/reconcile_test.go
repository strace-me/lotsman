package capture

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeRunner records nft invocations and can fail the APPLY (bare `nft -f`,
// i.e. -f without -c) while letting validation (`nft -c -f`) pass.
type fakeRunner struct {
	calls     [][]string
	failApply bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	hasF, hasC := false, false
	for _, a := range args {
		if a == "-f" {
			hasF = true
		}
		if a == "-c" {
			hasC = true
		}
	}
	if f.failApply && hasF && !hasC {
		return errors.New("fake nft -f fail")
	}
	return nil
}

func (f *fakeRunner) ran(sub string) bool {
	for _, c := range f.calls {
		for _, a := range c {
			if a == sub {
				return true
			}
		}
	}
	return false
}

func newRec(t *testing.T, run *fakeRunner, live func(context.Context) ([]byte, error)) *Reconciler {
	t.Helper()
	return &Reconciler{
		Model: DefaultModel(), RulesetPath: t.TempDir() + "/cap.nft", BackupDir: t.TempDir(),
		Runner: run, LiveTable: live, Log: discardLog(),
	}
}

func TestReconcileNoOpWhenParity(t *testing.T) {
	run := &fakeRunner{}
	r := newRec(t, run, func(context.Context) ([]byte, error) { return GenerateNft(DefaultModel()), nil })
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 0 {
		t.Errorf("parity must be a no-op (no nft calls), got %v", run.calls)
	}
}

func TestReconcileProposeOnlyValidatesButDoesNotApply(t *testing.T) {
	run := &fakeRunner{}
	// live differs from desired (empty live)
	r := newRec(t, run, func(context.Context) ([]byte, error) { return []byte("table ip singbox { }"), nil })
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !run.ran("-c") {
		t.Error("propose-only must validate with nft -c")
	}
	if run.ran("-f") && !hasCheckF(run) {
		t.Error("propose-only must NOT apply (no bare nft -f without -c)")
	}
}

// hasCheckF reports whether a `-f` appeared only alongside `-c` (validation), not a bare apply.
func hasCheckF(run *fakeRunner) bool {
	for _, c := range run.calls {
		hasF, hasC := false, false
		for _, a := range c {
			if a == "-f" {
				hasF = true
			}
			if a == "-c" {
				hasC = true
			}
		}
		if hasF && !hasC {
			return false // a bare apply happened
		}
	}
	return true
}

func TestReconcileArmedApplies(t *testing.T) {
	run := &fakeRunner{}
	calls := 0
	live := func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("table ip singbox { }"), nil // before: differs
		}
		return GenerateNft(DefaultModel()), nil // after apply: matches desired
	}
	r := newRec(t, run, live)
	r.Arm()
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !run.ran("-c") || !run.ran("-f") {
		t.Errorf("armed must validate (-c) AND apply (-f), calls=%v", run.calls)
	}
}

func TestReconcileArmedRollsBackOnApplyFailure(t *testing.T) {
	run := &fakeRunner{failApply: true} // apply (nft -f) fails; validate (-c -f) passes
	r := newRec(t, run, func(context.Context) ([]byte, error) { return []byte("table ip singbox { }"), nil })
	r.Arm()
	err := r.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("apply failure must roll back and error, got %v", err)
	}
}

// A clean nft -f that nonetheless leaves the table UNREADABLE (post-apply
// LiveTable errors — table gone) is the catastrophic case the verify exists for.
// It must roll back, not report success (previously err!=nil skipped the guard).
func TestReconcileArmedRollsBackWhenPostApplyTableUnreadable(t *testing.T) {
	run := &fakeRunner{} // apply (nft -f) itself succeeds
	calls := 0
	live := func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("table ip singbox { }"), nil // before: differs -> applies
		}
		return nil, errors.New("nft list table: No such file or directory") // table gone after load
	}
	r := newRec(t, run, live)
	r.Arm()
	err := r.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("unreadable post-apply table must roll back and error, got %v", err)
	}
}

func TestApplyScriptIsAtomicReplace(t *testing.T) {
	s := string(applyScript(DefaultModel()))
	if !strings.HasPrefix(s, "add table ip singbox\ndelete table ip singbox\n") {
		t.Errorf("apply script must start with atomic add+delete prelude:\n%s", s)
	}
	if !strings.Contains(s, "tproxy to 127.0.0.1:7893") {
		t.Error("apply script must contain the recreated table")
	}
}
