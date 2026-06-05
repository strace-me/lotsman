package zapretgen

import (
	"context"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// fakeRunner records the commands it is asked to run.
type fakeRunner struct {
	cmds      [][]string
	failOn    string // substring of a command that should error (e.g. "restart")
	failCount int    // how many matching commands still error before succeeding
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	cmd := append([]string{name}, args...)
	f.cmds = append(f.cmds, cmd)
	if f.failOn != "" && f.failCount > 0 && strings.Contains(strings.Join(cmd, " "), f.failOn) {
		f.failCount--
		return context.DeadlineExceeded
	}
	return nil
}

func (f *fakeRunner) symlinkTargets() []string {
	var out []string
	for _, c := range f.cmds {
		if len(c) >= 3 && c[0] == "ln" {
			out = append(out, c[len(c)-2]) // the target in `ln -sfn <target> <link>`
		}
	}
	return out
}

func discordArmed(t *testing.T, probe func(context.Context, string) bool, rec func(string, string, bool)) (*Reconciler, *fakeRunner) {
	t.Helper()
	svc := registry.Service{
		Name: "discord", Profile: "voice", ProbeTarget: "https://discord.com/api/v9/gateway",
		Domains: []string{"discord.media"},
		Chain:   []registry.ChainStep{{StrategyClass: strategy.ClassZapret}},
	}
	cat := []strategycat.Recipe{{ID: "disc-1", TargetClass: strategycat.ClassDiscordTCP, NfqwsArgs: []string{"--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"}}}
	r := New([]registry.Service{svc}, func(string) int { return 0 }, cat, zaptune.FirstPicker, "", quietLog())
	run := &fakeRunner{}
	r.Arm(ArmConfig{
		Runner: run, ComposedPath: t.TempDir() + "/composed.sh", ActiveLink: t.TempDir() + "/active.sh",
		RestartCmd: []string{"/etc/init.d/nfqws", "restart"}, LKGTarget: "/opt/zapret/alt12.sh",
		Probe: probe, Record: rec, CanaryProbes: 2,
	})
	return r, run
}

func TestArmCanaryPassCommitsLKG(t *testing.T) {
	var recorded []string
	r, run := discordArmed(t, func(context.Context, string) bool { return true }, func(svc, id string, ok bool) {
		recorded = append(recorded, svc+"/"+id+"/"+boolStr(ok))
	})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Applied: symlink -> composed, restart. No rollback.
	if got := run.symlinkTargets(); len(got) != 1 || !strings.HasSuffix(got[0], "composed.sh") {
		t.Errorf("expected one symlink to composed.sh, got %v", got)
	}
	if r.lkgTarget == "" || !strings.HasSuffix(r.lkgTarget, "composed.sh") {
		t.Errorf("canary pass should commit LKG = composed, got %q", r.lkgTarget)
	}
	if len(recorded) != 1 || recorded[0] != "discord/disc-1/ok" {
		t.Errorf("canary pass should record success, got %v", recorded)
	}
}

func TestArmCanaryFailRollsBackToLKGAndDemotes(t *testing.T) {
	// Reachable in baseline, unreachable after switch -> degraded -> rollback + fail-record.
	var calls int
	probe := func(context.Context, string) bool {
		calls++
		return calls == 1 // first (baseline) ok, all canary probes fail
	}
	var recorded []string
	r, run := discordArmed(t, probe, func(svc, id string, ok bool) { recorded = append(recorded, svc+"/"+id+"/"+boolStr(ok)) })
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Two symlinks: to composed (apply) then back to the LKG floor (rollback).
	got := run.symlinkTargets()
	if len(got) != 2 || !strings.HasSuffix(got[0], "composed.sh") || got[1] != "/opt/zapret/alt12.sh" {
		t.Errorf("expected apply then rollback-to-LKG, got %v", got)
	}
	// LKG must NOT advance to the failed composed config.
	if r.lkgTarget != "/opt/zapret/alt12.sh" {
		t.Errorf("canary fail must keep LKG at the floor, got %q", r.lkgTarget)
	}
	if len(recorded) != 1 || recorded[0] != "discord/disc-1/fail" {
		t.Errorf("canary fail should record failure (KB demote), got %v", recorded)
	}
}

func TestArmRestartFailureRollsBack(t *testing.T) {
	// The apply restart fails -> rollback to LKG (a second restart, which succeeds).
	r, run := discordArmed(t, func(context.Context, string) bool { return true }, nil)
	run.failOn, run.failCount = "restart", 1 // only the first restart fails
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("apply restart failure should return an error")
	}
	got := run.symlinkTargets()
	if len(got) != 2 || got[1] != "/opt/zapret/alt12.sh" {
		t.Errorf("restart failure should roll back to LKG, symlinks=%v", got)
	}
	if r.lkgTarget != "/opt/zapret/alt12.sh" {
		t.Errorf("LKG must stay at floor after restart failure, got %q", r.lkgTarget)
	}
}

func boolStr(b bool) string {
	if b {
		return "ok"
	}
	return "fail"
}
