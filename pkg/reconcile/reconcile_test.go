package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// seqLoader returns a different result on each Load call, cycling through results
// and repeating the last one once exhausted. Tracks the number of calls so a test
// can assert how many fetch attempts happened (LOT-29 retry).
type seqLoader struct {
	results []fakeLoader
	calls   int
}

func (s *seqLoader) Load(ctx context.Context, d []subscription.Declaration) ([]subscription.Node, []error) {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	return s.results[i].Load(ctx, d)
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

// LOT-35: a restart tears active UDP conntrack and breaks a live voice/RTC call.
// When ActiveRealtimeUDP reports a live flow, the forward apply is DEFERRED — the
// config validates (check) but is NOT swapped or restarted, and the next tick still
// sees the diff and retries. Once the call ends, the apply goes through.
func TestReconcileDefersRestartDuringActiveVoice(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	os.WriteFile(cfgPath, []byte(`{"old":true}`), 0o644)

	active := true
	r.ActiveRealtimeUDP = func(context.Context) (string, bool) { return "discord", active }

	// Pass 1: a live call is in progress → validate but defer the restart.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile (deferred): %v", err)
	}
	if !run.ran("check") {
		t.Error("deferred apply should still validate with sing-box check")
	}
	if run.ran("restart") {
		t.Error("must NOT restart while a live voice/RTC flow is in progress")
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != `{"old":true}` {
		t.Errorf("live config must be untouched while deferring, got %q", got)
	}

	// Pass 2: the call ended → the still-pending diff applies + restarts.
	run.calls = nil
	active = false
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile (call ended): %v", err)
	}
	if !run.ran("check") || !run.ran("restart") {
		t.Errorf("once the call ends the pending config must apply (check+restart), calls=%v", run.calls)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) == `{"old":true}` {
		t.Error("config should have been replaced once the call ended")
	}
}

// LOT-13 hardening: if the live config exists but is UNREADABLE (a real read
// error, not "absent"), Reconcile must refuse the tick — proceeding would treat
// live as empty, take no backup, and leave a failed restart with nothing to roll
// back to. A directory at ConfigPath makes os.ReadFile error with a non-IsNotExist
// error, simulating the unreadable case.
func TestReconcileRefusesUnreadableLiveConfig(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	if err := os.Mkdir(cfgPath, 0o755); err != nil {
		t.Fatalf("mkdir cfgPath: %v", err)
	}

	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected an error when the live config is unreadable")
	}
	if run.ran("check") || run.ran("restart") {
		t.Errorf("must not validate or restart when live config is unreadable, calls=%v", run.calls)
	}
}

// LOT-13: the maintenance ticker and the armed remediation commit both call
// Reconcile from separate goroutines. Concurrent passes must be serialized — run
// under -race to catch any unsynchronized last/lastNodes access, and assert that N
// concurrent callers against the same diff produce exactly ONE apply+restart (the
// first to grab the lock applies; the rest see the now-in-sync config and no-op),
// never a double-swap / double-restart.
func TestReconcileSerializesConcurrentCalls(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	os.WriteFile(cfgPath, []byte(`{"old":true}`), 0o644)

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = r.Reconcile(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Reconcile[%d] errored: %v", i, err)
		}
	}
	var restarts int
	for _, c := range run.calls {
		if len(c) > 0 && c[len(c)-1] == "restart" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Errorf("serialized concurrent reconcile must apply exactly once, got %d restarts (calls=%v)", restarts, run.calls)
	}
}

// LOT-13: the daemon OWNS the live config. A hand-edit between passes is drift —
// the next Reconcile regenerates desired, sees live differs, and re-applies,
// reverting the manual change. This documents that self-healing (the audit noted
// Alive() doesn't "see" edits; the desired-vs-live compare reverts them anyway).
func TestReconcileRevertsManualEdit(t *testing.T) {
	run := &fakeRunner{}
	r, cfgPath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	os.WriteFile(cfgPath, []byte(`{"old":true}`), 0o644)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	desired, _ := os.ReadFile(cfgPath)
	if len(desired) == 0 {
		t.Fatal("first reconcile did not write the config")
	}

	// Someone hand-edits the live config out from under the daemon.
	os.WriteFile(cfgPath, []byte(`{"hand":"edited"}`), 0o644)
	run.calls = nil

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after manual edit: %v", err)
	}
	if !run.ran("restart") {
		t.Errorf("manual edit (drift from desired) must trigger a re-apply, calls=%v", run.calls)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != string(desired) {
		t.Errorf("manual edit must be reverted to desired; got %q", got)
	}
}

// Regression guard (LOT-18b): an empty/nil Remediations provider must leave the
// generated config byte-identical to no provider at all, so an armed-but-idle
// controller never causes a spurious apply/restart.
func TestReconcileEmptyRemediationsAreNoOp(t *testing.T) {
	// Baseline: reconcile with no provider, capture the written config.
	run := &fakeRunner{}
	rBase, basePath := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	if err := rBase.Reconcile(context.Background()); err != nil {
		t.Fatalf("baseline reconcile: %v", err)
	}
	base, _ := os.ReadFile(basePath)

	// With a provider that returns nil and one that returns an empty map, the
	// generated config must be identical to the baseline.
	for name, provider := range map[string]func() map[string]singbox.Remediation{
		"nil-map":   func() map[string]singbox.Remediation { return nil },
		"empty-map": func() map[string]singbox.Remediation { return map[string]singbox.Remediation{} },
	} {
		run2 := &fakeRunner{}
		r, path := testReconciler(t, run2, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
		r.Remediations = provider
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("%s reconcile: %v", name, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != string(base) {
			t.Errorf("%s: config differs from baseline (must be byte-identical)", name)
		}
	}

	// And a NON-empty provider DOES change the config (proving the seam is wired).
	run3 := &fakeRunner{}
	r, path := testReconciler(t, run3, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	r.Remediations = func() map[string]singbox.Remediation {
		return map[string]singbox.Remediation{"youtube": {RejectQUIC: true}}
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("active-remediation reconcile: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) == string(base) {
		t.Error("active remediation did not change the generated config")
	}
	if !run3.ran("check") || !run3.ran("restart") {
		t.Errorf("active remediation must apply (check+restart), calls=%v", run3.calls)
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

// LOT-27: the vpn-a subscription intermittently returns 0 nodes on a SUCCESSFUL
// (error-free) fetch; applying it restarts sing-box into a node-less config and
// drops every live connection (Discord gateway etc.). The dramatic-drop clause
// must skip it even though there is no fetch error.
func TestReconcileSkipsNodeCollapseWithoutError(t *testing.T) {
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// Error-free fetch returning zero nodes (the vpn-a flap) → must skip.
	r.Loader = fakeLoader{nodes: nil}
	run.calls = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("error-free node collapse must skip (no check/restart), calls=%v", run.calls)
	}
}

// nodesN builds n distinct reality nodes (distinct servers) so the set has a real
// size for the anti-churn baseline/shrink logic.
func nodesN(t *testing.T, n int) []subscription.Node {
	t.Helper()
	out := make([]subscription.Node, n)
	for i := 0; i < n; i++ {
		out[i] = realityNode(t, fmt.Sprintf("10.0.0.%d", i+1), "sid")
	}
	return out
}

// LOT-29 part 1: a Reconciler seeded from a baseline file containing N has
// lastNodes==N BEFORE the first Reconcile, so a degraded first fetch (few/0
// nodes) is skipped — no apply — even on the very first post-restart tick.
func TestReconcileBaselineFileSkipsDegradedFirstFetch(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "baseline.json")
	bs := NewBaselineStore(basePath)
	if err := bs.Save(10); err != nil {
		t.Fatalf("save baseline: %v", err)
	}

	run := &fakeRunner{}
	// Degraded startup fetch: 0 nodes (the deploy that landed a degraded config).
	r, _ := testReconciler(t, run, fakeLoader{nodes: nil}, false)
	r.Baseline = NewBaselineStore(basePath)
	r.SetBaseline(r.Baseline.Load())
	r.FetchBackoff = time.Millisecond // keep retries instant
	if r.lastNodes != 10 {
		t.Fatalf("seeded lastNodes = %d, want 10", r.lastNodes)
	}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("degraded first fetch with persisted baseline must skip, calls=%v", run.calls)
	}
}

// LOT-29 part 1 (negative): with NO baseline file, the first reconcile has
// lastNodes==0, so the genuine first-ever apply proceeds even with a small set.
func TestReconcileNoBaselineFirstApplyProceeds(t *testing.T) {
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: []subscription.Node{node(t)}}, false)
	if r.lastNodes != 0 {
		t.Fatalf("cold lastNodes = %d, want 0", r.lastNodes)
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !run.ran("check") || !run.ran("restart") {
		t.Errorf("first-ever apply must proceed, calls=%v", run.calls)
	}
}

// LOT-29 part 1: a clean apply persists the baseline to the file so the next
// process restart can restore it.
func TestReconcilePersistsBaselineOnApply(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "baseline.json")
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: nodesN(t, 6)}, false)
	r.Baseline = NewBaselineStore(basePath)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := NewBaselineStore(basePath).Load(); got != 6 {
		t.Errorf("persisted baseline = %d, want 6", got)
	}
}

// LOT-29 part 2: a transient empty fetch followed by a full set — reconcile
// retries within the tick and applies the good set (does NOT wait for the next
// tick / does not skip).
func TestReconcileRetriesTransientThenApplies(t *testing.T) {
	run := &fakeRunner{}
	good := nodesN(t, 6)
	r, _ := testReconciler(t, run, fakeLoader{}, false)
	r.SetBaseline(6) // we have a baseline (as if restored)
	r.FetchBackoff = time.Millisecond
	r.Loader = &seqLoader{results: []fakeLoader{
		{nodes: nil},  // attempt 1: transient empty
		{nodes: nil},  // attempt 2: still empty
		{nodes: good}, // attempt 3: recovered full set
	}}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !run.ran("check") || !run.ran("restart") {
		t.Errorf("retry should land the good set and apply, calls=%v", run.calls)
	}
	if sl := r.Loader.(*seqLoader); sl.calls < 3 {
		t.Errorf("expected >=3 fetch attempts (retry), got %d", sl.calls)
	}
}

// LOT-29 part 2: a loader that stays degraded for every attempt — after the
// bounded retries reconcile skips (keeps last-good), no apply.
func TestReconcileRetriesStayDegradedThenSkips(t *testing.T) {
	run := &fakeRunner{}
	r, _ := testReconciler(t, run, fakeLoader{nodes: nil}, false)
	r.SetBaseline(6)
	r.FetchRetries = 3
	r.FetchBackoff = time.Millisecond

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("persistently degraded fetch must skip after retries, calls=%v", run.calls)
	}
}
