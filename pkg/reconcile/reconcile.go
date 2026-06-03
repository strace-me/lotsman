// Package reconcile makes the daemon the owner of the sing-box routing config.
// It computes the DESIRED config from the lotsman config + live subscriptions
// (via pkg/singbox), compares it to the live file, and — only on a real
// difference — validates it with `sing-box check` and swaps it in, restarting
// sing-box with a backup it can roll back to. Selector steering (which pool a
// service uses) stays with the Brain/vpnbalance over the Clash API and needs no
// restart; this reconciler owns the *structure* (which nodes/pools/routes exist).
//
// Safety rails: it never applies a config that fails `sing-box check`; it skips
// a tick whose subscription fetch degraded the node set (anti-churn on a flaky
// mirror); it backs up and rolls back if sing-box does not come back alive; and
// it defaults to dry-run so a deployment can watch what it *would* do first.
package reconcile

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/strace-me/lotsman/pkg/executor"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// Loader fetches and normalizes subscription nodes. subscription.Manager
// implements it; abstracted so the reconciler is testable without network.
type Loader interface {
	Load(ctx context.Context, decls []subscription.Declaration) ([]subscription.Node, []error)
}

// Reconciler regenerates and (in real mode) applies the sing-box config.
type Reconciler struct {
	Services []registry.Service
	Devices  []registry.Device
	Opts     singbox.Options
	Subs     []subscription.Declaration
	Loader   Loader
	Pools    *pools.Set

	ConfigPath string // path to the live sing-box config the daemon owns
	BackupDir  string // where pre-apply backups go ("" = no backup)
	DryRun     bool   // true = compute + validate + log, never write/restart

	Runner     executor.Runner
	SingboxBin string                     // binary for `<bin> check -c <path>` (e.g. "sing-box")
	RestartCmd []string                   // name + args, e.g. ["/etc/init.d/sing-box","restart"]
	Alive      func(context.Context) bool // post-restart health (nil = skip the check)
	Log        *slog.Logger

	last      []byte // last config bytes we wrote/observed (skip re-validation)
	lastNodes int    // node count of the last clean apply (anti-churn baseline)
}

// Reconcile runs one pass. It is safe to call on a ticker; it is a no-op when
// the desired config already matches the live file.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	nodes, errs := r.Loader.Load(ctx, r.Subs)
	for _, e := range errs {
		r.Log.Warn("reconcile: subscription load issue", "err", e)
	}
	// Anti-churn: a flaky mirror that drops nodes must not trigger a restart to a
	// smaller config that the next tick undoes. Skip only when the fetch errored
	// AND the set shrank versus the last clean apply.
	if len(errs) > 0 && len(nodes) < r.lastNodes {
		r.Log.Warn("reconcile: skipping, degraded node set", "nodes", len(nodes), "last", r.lastNodes)
		return nil
	}

	memberships := r.Pools.Memberships(nodes)
	opts := r.Opts
	opts.PoolOpts = singbox.PoolOptionsFrom(r.Pools)
	res, err := singbox.Generate(r.Services, r.Devices, nodes, memberships, opts)
	if err != nil {
		return fmt.Errorf("reconcile: generate: %w", err)
	}
	desired := res.JSON
	for _, k := range res.SkippedKnobs {
		r.Log.Warn("reconcile: knob skipped for target sing-box version", "knob", k)
	}

	live, _ := os.ReadFile(r.ConfigPath)
	if bytes.Equal(desired, live) {
		r.last = desired
		r.lastNodes = len(nodes)
		return nil // already in sync
	}

	// Validate before touching anything — never apply a config sing-box rejects.
	tmp := r.ConfigPath + ".lotsman-tmp"
	if err := os.WriteFile(tmp, desired, 0o644); err != nil {
		return fmt.Errorf("reconcile: write tmp: %w", err)
	}
	if err := r.check(ctx, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("reconcile: generated config failed sing-box check (not applied): %w", err)
	}

	if r.DryRun {
		os.Remove(tmp)
		r.Log.Info("reconcile: config differs and passes check (dry-run, not applied)",
			"nodes", len(nodes), "pools", len(memberships), "skipped", len(res.Skipped))
		return nil
	}

	if err := r.apply(ctx, tmp, live); err != nil {
		return err
	}
	r.last = desired
	r.lastNodes = len(nodes)
	r.Log.Info("reconcile: applied new sing-box config", "nodes", len(nodes), "pools", len(memberships))
	return nil
}

// apply swaps tmp into place with a backup, restarts sing-box, and rolls back to
// the previous config if sing-box does not come back alive.
func (r *Reconciler) apply(ctx context.Context, tmp string, live []byte) error {
	if r.BackupDir != "" && len(live) > 0 {
		backup := filepath.Join(r.BackupDir, "config.json."+time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, live, 0o644); err != nil {
			r.Log.Warn("reconcile: backup failed (continuing)", "err", err)
		} else {
			r.Log.Info("reconcile: backed up current config", "path", backup)
		}
	}
	if err := os.Rename(tmp, r.ConfigPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("reconcile: swap config: %w", err)
	}
	if err := r.restart(ctx); err != nil {
		return r.rollback(ctx, live, fmt.Errorf("restart failed: %w", err))
	}
	if r.Alive != nil && !r.Alive(ctx) {
		return r.rollback(ctx, live, fmt.Errorf("sing-box not alive after restart"))
	}
	return nil
}

// rollback restores the previous config and restarts, returning the cause error
// wrapped (so the caller sees both the failure and the rollback outcome).
func (r *Reconciler) rollback(ctx context.Context, live []byte, cause error) error {
	if len(live) == 0 {
		return fmt.Errorf("reconcile: %w; no backup to roll back to", cause)
	}
	if err := os.WriteFile(r.ConfigPath, live, 0o644); err != nil {
		return fmt.Errorf("reconcile: %w; ROLLBACK WRITE FAILED: %v", cause, err)
	}
	if err := r.restart(ctx); err != nil {
		return fmt.Errorf("reconcile: %w; ROLLBACK RESTART FAILED: %v", cause, err)
	}
	r.Log.Warn("reconcile: rolled back to previous config", "cause", cause)
	return fmt.Errorf("reconcile: %w (rolled back)", cause)
}

func (r *Reconciler) check(ctx context.Context, path string) error {
	bin := r.SingboxBin
	if bin == "" {
		bin = "sing-box"
	}
	return r.Runner.Run(ctx, bin, "check", "-c", path)
}

func (r *Reconciler) restart(ctx context.Context) error {
	if len(r.RestartCmd) == 0 {
		return fmt.Errorf("no restart command configured")
	}
	return r.Runner.Run(ctx, r.RestartCmd[0], r.RestartCmd[1:]...)
}
