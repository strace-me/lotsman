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
	"encoding/json"
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

	// Remediations supplies the armed controller's active per-service self-heal
	// remediations (LOT-18b), read fresh on every Reconcile and folded into the
	// generated config via opts.Remediations. nil (or a nil/empty return) means
	// no remediations are active, in which case the generated config is
	// byte-identical to today (LOT-18a guarantees this for empty Remediations).
	// The controller updates the backing map and calls Reconcile to apply/revert.
	Remediations func() map[string]singbox.Remediation

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
	// smaller config the next tick undoes. Skip when the set shrank versus the last
	// clean apply AND either the fetch errored OR the drop is dramatic (>50%). The
	// acme subscription intermittently returns 0 nodes on a SUCCESSFUL (error-free)
	// fetch; applying that empties the VPN pool and restarts sing-box into a
	// node-less config — dropping every live connection (Discord gateway etc.). The
	// dramatic-drop clause catches that case the error-only guard missed (LOT-27).
	if r.lastNodes > 0 && len(nodes) < r.lastNodes && (len(errs) > 0 || len(nodes)*2 < r.lastNodes) {
		r.Log.Warn("reconcile: skipping, degraded node set", "nodes", len(nodes), "last", r.lastNodes, "fetch_errs", len(errs))
		return nil
	}

	memberships := r.Pools.Memberships(nodes)
	opts := r.Opts
	opts.PoolOpts = singbox.PoolOptionsFrom(r.Pools)
	if r.Remediations != nil {
		// Fold in the armed controller's active remediations (LOT-18b). When none
		// are active this is nil/empty and generation is byte-identical to today.
		opts.Remediations = r.Remediations()
	}
	res, err := singbox.Generate(r.Services, r.Devices, nodes, memberships, opts)
	if err != nil {
		return fmt.Errorf("reconcile: generate: %w", err)
	}
	desired := res.JSON
	for _, k := range res.SkippedKnobs {
		r.Log.Warn("reconcile: knob skipped for target sing-box version", "knob", k)
	}

	live, _ := os.ReadFile(r.ConfigPath)
	if sameConfig(desired, live) {
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

// volatileKeys are config fields the VPN provider rotates for the same nodes
// (same servers/uuids/order) without any real change. Comparing them byte-for-
// byte makes reconcile re-apply + restart sing-box on cosmetic churn (LOT-1).
// REALITY short_id is rotated by the `acme` provider every few minutes.
var volatileKeys = map[string]bool{
	"short_id": true,
}

// sameConfig reports whether two generated sing-box configs are semantically
// equal, ignoring volatile fields (see volatileKeys). It normalizes both sides
// by blanking those fields in the parsed JSON and comparing the canonical forms.
// If either side cannot be parsed, it falls back to a raw byte comparison so a
// real change is never silently skipped.
func sameConfig(desired, live []byte) bool {
	dn, derr := normalizeConfig(desired)
	ln, lerr := normalizeConfig(live)
	if derr != nil || lerr != nil {
		return bytes.Equal(desired, live)
	}
	return bytes.Equal(dn, ln)
}

// normalizeConfig parses the config and returns a canonical JSON form with every
// volatile field blanked out, so configs differing only in those fields compare
// equal. Map-key ordering is canonical because encoding/json sorts object keys.
func normalizeConfig(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty config")
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	blankVolatile(v)
	return json.Marshal(v)
}

// blankVolatile walks the decoded JSON tree and clears any value whose key is in
// volatileKeys, neutralizing provider-rotated fields before comparison.
func blankVolatile(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if volatileKeys[k] {
				t[k] = ""
			} else {
				blankVolatile(t[k])
			}
		}
	case []any:
		for _, e := range t {
			blankVolatile(e)
		}
	}
}
