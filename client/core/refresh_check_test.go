package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// checkBox records what the ProxyCore was asked to validate.
type checkBox struct {
	ProxyCore
	got []byte
	err error
}

func (b *checkBox) Check(_ context.Context, cfg []byte) error { b.got = cfg; return b.err }

// An embedded core (Android/libbox) has no sing-box binary, so the reconciler's
// `check` must be validated in-process. Exec'ing the empty binary name instead failed
// EVERY refresh tick, and the reconciler reads a failed check as "do not apply" — so
// subscriptions would silently stop being applied, with nothing saying why.
func TestCheckWithoutABinaryValidatesInProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "singbox.json")
	want := []byte(`{"log":{"level":"warn"}}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	box := &checkBox{}
	if err := (boxRunner{box: box}).Run(context.Background(), "", "check", "-c", path); err != nil {
		t.Fatalf("in-process check: %v", err)
	}
	if string(box.got) != string(want) {
		t.Errorf("ProxyCore.Check got %q, want the candidate config %q", box.got, want)
	}
}

func TestCheckWithoutABinaryReportsARejectedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "singbox.json")
	os.WriteFile(path, []byte("{}"), 0o600)
	box := &checkBox{err: errors.New("bad config")}
	if err := (boxRunner{box: box}).Run(context.Background(), "", "check", "-c", path); err == nil {
		t.Error("a config the core rejects must fail the check, or the reconciler would apply it")
	}
}

// A malformed invocation is a caller bug, not a bad config — it must not be reported
// as one, or an operator would hunt a config that is perfectly fine.
func TestCheckWithoutABinaryRejectsAMissingPath(t *testing.T) {
	box := &checkBox{}
	if err := (boxRunner{box: box}).Run(context.Background(), "", "check"); err == nil {
		t.Error("check with no -c must error")
	}
	if box.got != nil {
		t.Error("nothing should have been validated")
	}
}
