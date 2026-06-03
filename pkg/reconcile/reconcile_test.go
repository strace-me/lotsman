package reconcile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/subscription"
)

type fakeRunner struct {
	calls      [][]string
	checkErr   error
	restartErr error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	if len(args) > 0 && args[0] == "check" {
		return f.checkErr
	}
	return f.restartErr
}

func (f *fakeRunner) ran(kind string) bool {
	for _, c := range f.calls {
		for _, a := range c {
			if a == kind {
				return true
			}
		}
	}
	return false
}

type fakeLoader struct {
	nodes []subscription.Node
	errs  []error
}

func (f fakeLoader) Load(context.Context, []subscription.Declaration) ([]subscription.Node, []error) {
	return f.nodes, f.errs
}

func node(t *testing.T) subscription.Node {
	t.Helper()
	ns, err := subscription.Parse([]byte("hysteria2://pw@1.2.3.4:443?sni=x.example"), subscription.FormatSingleURL, "test")
	if err != nil || len(ns) != 1 {
		t.Fatalf("parse node: %v", err)
	}
	return ns[0]
}

// realityNode builds a VLESS+REALITY node with the given server and short_id.
// short_id is the field the provider rotates on the same node (LOT-1).
func realityNode(t *testing.T, server, sid string) subscription.Node {
	t.Helper()
	url := "vless://11111111-2222-3333-4444-555555555555@" + server +
		":443?security=reality&flow=xtls-rprx-vision&pbk=PUBKEY&sid=" + sid + "&sni=www.example.com&fp=chrome#n"
	ns, err := subscription.Parse([]byte(url), subscription.FormatSingleURL, "test")
	if err != nil || len(ns) != 1 {
		t.Fatalf("parse reality node: %v", err)
	}
	return ns[0]
}

func testReconciler(t *testing.T, run *fakeRunner, ld fakeLoader, dryRun bool) (*Reconciler, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	return &Reconciler{
		Services: []registry.Service{{
			Name: "youtube", RuleSets: []string{"geosite-youtube"},
			Chain: []registry.ChainStep{{Position: 0, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"}},
		}},
		Opts:   singbox.DefaultOptions(),
		Subs:   []subscription.Declaration{{Name: "s", Enabled: true}},
		Loader: ld,
		Pools: &pools.Set{Pools: map[string]pools.Pool{
			"vpn_url_test": {Name: "vpn_url_test", Type: pools.TypeURLTest, Filter: pools.Filter{Caps: []string{pools.CapTCP}}},
		}},
		ConfigPath: cfgPath,
		BackupDir:  dir,
		DryRun:     dryRun,
		Runner:     run,
		SingboxBin: "sing-box",
		RestartCmd: []string{"/etc/init.d/sing-box", "restart"},
		Alive:      func(context.Context) bool { return true },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, cfgPath
}

func TestReconcileDryRunDoesNotApply(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, true)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Error("dry-run must not write the config")
	}
	if run.ran("restart") {
		t.Error("dry-run must not restart")
	}
	if !run.ran("check") {
		t.Error("dry-run should still validate with sing-box check")
	}
}

func TestReconcileAppliesAndBacksUp(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	// seed a different live config so there IS a diff + something to back up.
	os.WriteFile(cfgPath, []byte(`{"old":true}`), 0o644)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, _ := os.ReadFile(cfgPath)
	if len(got) == 0 || string(got) == `{"old":true}` {
		t.Error("config was not replaced with generated desired")
	}
	if !run.ran("check") || !run.ran("restart") {
		t.Errorf("expected check+restart, calls=%v", run.calls)
	}
	// a backup of the old config exists.
	entries, _ := os.ReadDir(filepath.Dir(cfgPath))
	var backups int
	for _, e := range entries {
		if e.Name() != "config.json" {
			backups++
		}
	}
	if backups == 0 {
		t.Error("expected a backup of the previous config")
	}
}

func TestReconcileNoOpWhenInSync(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	// First apply establishes the desired config on disk.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	_ = cfgPath
	run.calls = nil
	// Second pass: live already == desired → no check, no restart.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("in-sync reconcile must be a no-op, calls=%v", run.calls)
	}
}

func TestReconcileRejectsConfigThatFailsCheck(t *testing.T) {
	run := &fakeRunner{checkErr: errors.New("bad config")}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	os.WriteFile(cfgPath, []byte(`{"old":true}`), 0o644)

	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected error when sing-box check fails")
	}
	if run.ran("restart") {
		t.Error("must not restart when check fails")
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != `{"old":true}` {
		t.Error("config must be untouched when check fails")
	}
}

func TestReconcileRollsBackWhenRestartFails(t *testing.T) {
	run := &fakeRunner{restartErr: errors.New("restart boom")}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	os.WriteFile(cfgPath, []byte(`{"old":true}`), 0o644)

	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected error when restart fails")
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != `{"old":true}` {
		t.Errorf("config must be rolled back to previous on restart failure, got %q", got)
	}
}

// LOT-1: the provider rotates REALITY short_id on the same nodes every few
// minutes. A config that differs ONLY in short_id must be treated as in-sync —
// no re-apply, no sing-box restart (which would drop all live connections).
func TestReconcileIgnoresShortIDRotation(t *testing.T) {
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{realityNode(t, "1.2.3.4", "aaaa")}}, false)
	// First apply establishes the live config on disk (with short_id=aaaa).
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !run.ran("restart") {
		t.Fatalf("first apply should restart, calls=%v", run.calls)
	}
	// Provider rotates short_id on the SAME node (same server/uuid/order).
	r.Loader = fakeLoader{nodes: []subscription.Node{realityNode(t, "1.2.3.4", "bbbb")}}
	run.calls = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("short_id-only churn must be a no-op (no check/restart), calls=%v", run.calls)
	}
}

// A real change (different server) must still trigger an apply + restart, so the
// short_id normalization does not mask genuine node changes.
func TestReconcileAppliesOnRealChange(t *testing.T) {
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{realityNode(t, "1.2.3.4", "aaaa")}}, false)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// New server (same short_id) — a genuine change that must be applied.
	r.Loader = fakeLoader{nodes: []subscription.Node{realityNode(t, "9.9.9.9", "aaaa")}}
	run.calls = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !run.ran("check") || !run.ran("restart") {
		t.Errorf("real node change must apply (check+restart), calls=%v", run.calls)
	}
}

func TestReconcileSkipsDegradedNodeSet(t *testing.T) {
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	// First clean apply sets the node baseline (lastNodes=1).
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// Now a flaky fetch: error + zero nodes (< baseline) → must skip.
	r.Loader = fakeLoader{nodes: nil, errs: []error{errors.New("mirror down")}}
	run.calls = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("degraded fetch must skip (no check/restart), calls=%v", run.calls)
	}
}
