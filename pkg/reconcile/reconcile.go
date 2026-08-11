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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

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

// Runner runs a system command (the sing-box `check` and the init-script
// restart). Declared here — structurally identical to executor.Runner, which
// the daemon still satisfies via executor.ExecRunner — so this package stays off
// os/exec and can build for mobile targets that reuse its structure/coherence
// logic (executor imports os/exec, which gomobile rejects).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
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

	Runner     Runner
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

	// ActiveRealtimeUDP, when set, reports whether a live voice/RTC UDP flow is
	// currently in progress (and one such service name, for logging). A sing-box
	// restart tears active UDP conntrack/NAT, breaking a live call until the client
	// renegotiates (LOT-35). When this returns true, the forward apply is DEFERRED
	// (the live config is left untouched and retried next tick) so the new config
	// lands only once the call ends. It does NOT gate rollback (a bad config must
	// revert) or engine-health restarts (a wedged engine's flows are dead anyway, so
	// they would not report active). nil = never defer (preserves prior behavior).
	ActiveRealtimeUDP func(context.Context) (string, bool)

	// TunExcludes, when set, recomputes the tun's route_exclude_address on EVERY
	// reconcile instead of using the value snapshotted into Opts. It exists because
	// those routes are a fact about the machine's CURRENT network — the subnets
	// auto_route must not swallow — and a long-lived reconciler holds Opts from the
	// moment it was built. On a laptop that means the office LAN stays excluded and
	// the home LAN gets pulled into the tunnel, so the machine is unreachable on its
	// own network until the process restarts; worse, the periodic refresh keeps
	// re-asserting the stale set. Measured on the ThinkPad the day it became a daily
	// driver. nil = use Opts as given (the router, whose subnets do not move).
	TunExcludes func() []string

	// Baseline persists lastNodes across restarts so the anti-churn guard works on
	// the very first reconcile after a restart (otherwise lastNodes resets to 0 and
	// a degraded startup fetch is applied wholesale — LOT-29). nil = in-memory only.
	// Construct with NewBaselineStore and seed lastNodes from its Load before the
	// first Reconcile (see cmd/lotsmand wiring).
	Baseline *BaselineStore

	// FetchRetries / FetchBackoff bound the startup-fetch retry (LOT-29 part 2):
	// when a baseline exists and a fetch comes back degraded, re-Load up to
	// FetchRetries times with FetchBackoff between tries before giving up for the
	// tick. Zero values fall back to defaults (defaultFetchRetries/Backoff).
	FetchRetries int
	FetchBackoff time.Duration

	// mu serializes Reconcile so the periodic maintenance loop and the armed
	// remediation controller's commit (both call rc.Reconcile from separate
	// goroutines) never apply concurrently — that would race last/lastNodes and
	// could double-swap the config + double-restart sing-box (LOT-13).
	mu        sync.Mutex
	last      []byte // last config bytes we wrote/observed (skip re-validation)
	lastNodes int    // node count of the last clean apply (anti-churn baseline)
}

const (
	defaultFetchRetries = 3
	defaultFetchBackoff = 2 * time.Second
)

// ErrDeferred is returned by Reconcile when a config change validated but was
// deliberately NOT applied because a live voice/RTC flow is in progress (LOT-35):
// the apply is postponed to a later tick. A caller that treats a commit as an
// applied remediation (the armed controller) MUST distinguish this from success —
// nothing was written — so it stays idle and retries instead of entering canary
// on a config that never landed. Maintenance callers treat it as a benign no-op.
var ErrDeferred = errors.New("reconcile: apply deferred (active voice/RTC flow)")

// ErrNotApplied is ErrDeferred's sibling for the other paths that deliberately write
// NOTHING: a degraded node set (a flaky mirror must not churn the config) and dry-run.
// They used to return a bare nil, so every caller read "skipped" as "applied" — the
// caller then believes the live box carries the config it just built, which is how a
// reload reports success over an unchanged data plane and an armed controller enters
// canary on a config that never landed. Maintenance callers treat it as a benign no-op.
var ErrNotApplied = errors.New("reconcile: nothing applied")

// Reconcile runs one pass. It is safe to call on a ticker; it is a no-op when
// the desired config already matches the live file. Calls are serialized (LOT-13):
// the maintenance ticker and the armed remediation commit both reach this from
// separate goroutines, and overlapping passes would race last/lastNodes and could
// double-swap the config + double-restart sing-box. The lock is held for the whole
// pass (fetch/check/restart) — a concurrent caller waits its turn rather than
// interleaving, which is exactly the desired behavior for config mutation.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	nodes, errs := r.Loader.Load(ctx, r.Subs)
	for _, e := range errs {
		r.Log.Warn("reconcile: subscription load issue", "err", e)
	}
	// Anti-churn: a flaky mirror that drops nodes must not trigger a restart to a
	// smaller config the next tick undoes. When we have a baseline and the fetch is
	// degraded (errored or dramatically shrank — see degraded), retry the fetch a
	// few times with a short backoff to ride out a transient empty fetch and land
	// on a good set within this tick instead of waiting a whole -check-interval
	// (LOT-29). Only with a baseline: a genuine first-ever apply (lastNodes==0) must
	// not be blocked or retried. If still degraded after the retries, skip and keep
	// the last-good config — the same safety the in-tick guard gives mid-run (the
	// vpn-a subscription intermittently returns 0 nodes even on an error-free fetch,
	// which would otherwise empty the VPN pool and drop every live connection).
	if r.lastNodes > 0 && r.degraded(nodes, errs) {
		retries, backoff := r.FetchRetries, r.FetchBackoff
		if retries == 0 {
			retries = defaultFetchRetries
		}
		if backoff == 0 {
			backoff = defaultFetchBackoff
		}
		for i := 0; i < retries && r.degraded(nodes, errs); i++ {
			r.Log.Warn("reconcile: degraded fetch, retrying", "attempt", i+1, "nodes", len(nodes), "last", r.lastNodes, "fetch_errs", len(errs))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			nodes, errs = r.Loader.Load(ctx, r.Subs)
			for _, e := range errs {
				r.Log.Warn("reconcile: subscription load issue", "err", e)
			}
		}
		if r.degraded(nodes, errs) {
			r.Log.Warn("reconcile: skipping, degraded node set", "nodes", len(nodes), "last", r.lastNodes, "fetch_errs", len(errs))
			return fmt.Errorf("%w: degraded node set (%d nodes, %d fetch errors)", ErrNotApplied, len(nodes), len(errs))
		}
	}

	memberships := r.Pools.Memberships(nodes)
	opts := r.Opts
	opts.PoolOpts = singbox.PoolOptionsFrom(r.Pools)
	if r.TunExcludes != nil && opts.Tun != nil {
		// Copy the struct: opts is a shallow copy, so Tun still points at the caller's
		// value and writing through it would edit what every other reader sees.
		tun := *opts.Tun
		tun.ExcludeRoutes = r.TunExcludes()
		opts.Tun = &tun
	}
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

	live, rerr := os.ReadFile(r.ConfigPath)
	if rerr != nil && !os.IsNotExist(rerr) {
		// A genuine read error (not "file absent") means a config likely DOES exist
		// but we can't see it. Proceeding would treat live as empty: no backup is
		// taken and a failed restart has nothing to roll back to (rollback bails on
		// len(live)==0). Refuse the tick instead of applying blind (LOT-13 hardening).
		return fmt.Errorf("reconcile: read live config %s: %w (refusing to apply blind)", r.ConfigPath, rerr)
	}
	if sameConfig(desired, live) {
		r.last = desired
		r.setLastNodes(len(nodes))
		return nil // already in sync
	}

	// Validate before touching anything — never apply a config sing-box rejects.
	tmp := r.ConfigPath + ".lotsman-tmp"
	if err := os.WriteFile(tmp, desired, configMode(r.ConfigPath)); err != nil {
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
		return fmt.Errorf("%w: dry-run", ErrNotApplied)
	}

	// LOT-35: a restart tears active UDP conntrack/NAT and breaks a live voice/RTC
	// call until the client renegotiates. If one is in progress, leave the live config
	// untouched and retry next tick — the new config lands once the call ends. The
	// check is self-correcting (a wedged path has only dead UDP flows, so it never
	// blocks a recovery restart), and r.last is left unchanged so this keeps retrying.
	if r.ActiveRealtimeUDP != nil {
		if svc, active := r.ActiveRealtimeUDP(ctx); active {
			os.Remove(tmp)
			r.Log.Info("reconcile: deferring sing-box restart, active voice/RTC flow in progress (retry next tick)",
				"service", svc, "nodes", len(nodes))
			// Signal "not applied" (not success): the armed controller must not
			// treat this as an applied remediation and enter canary (LOT-35 fix).
			return ErrDeferred
		}
	}

	if err := r.apply(ctx, tmp, live); err != nil {
		return err
	}
	r.last = desired
	r.setLastNodes(len(nodes))
	r.Log.Info("reconcile: applied new sing-box config", "nodes", len(nodes), "pools", len(memberships))
	return nil
}

// degraded reports whether a fetch result is too poor to apply versus the
// last-good baseline: the set shrank AND either the fetch errored or the drop is
// dramatic (>50%). Centralizes the LOT-27 anti-churn condition so the in-tick
// guard and the startup retry (LOT-29) share one threshold. With no baseline
// (lastNodes==0) nothing is "degraded" — the first-ever apply is never blocked.
func (r *Reconciler) degraded(nodes []subscription.Node, errs []error) bool {
	if r.lastNodes <= 0 || len(nodes) >= r.lastNodes {
		return false
	}
	return len(errs) > 0 || len(nodes)*2 < r.lastNodes
}

// SetBaseline seeds the in-memory anti-churn baseline (lastNodes) without
// persisting — used at construction to restore the value loaded from
// BaselineStore so the degraded-fetch guard works on the first post-restart
// reconcile (LOT-29).
func (r *Reconciler) SetBaseline(n int) { r.lastNodes = n }

// setLastNodes updates the anti-churn baseline and persists it best-effort (like
// kb/state): a save failure only warns — the in-memory value still works for the
// running process, and a missing/corrupt file is treated as a cold start on the
// next boot.
func (r *Reconciler) setLastNodes(n int) {
	r.lastNodes = n
	if r.Baseline == nil {
		return
	}
	if err := r.Baseline.Save(n); err != nil {
		r.Log.Warn("reconcile: baseline save failed (continuing)", "err", err)
	}
}

// configMode inherits the live config's permissions. The file carries the
// Clash-API secret and every node credential, so a client that deliberately
// created it 0600 must not have it silently widened by a reconcile. 0644 only
// when there is no file to learn from, matching the router's historical mode.
func configMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o644
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
			r.pruneBackups()
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
	if err := os.WriteFile(r.ConfigPath, live, configMode(r.ConfigPath)); err != nil {
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
// REALITY short_id is rotated by the `vpn-a` provider every few minutes.
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

// keepBackups is how many pre-apply configs are retained. The names sort
// chronologically, so "newest N" is the tail of a sorted list.
//
// Ten because the backup answers "what did the last few applies look like" — a
// question about the recent past, not an archive. Unpruned it was neither: the
// router accumulated dozens over June and, at twelve applies an hour before
// LOT-1b was fixed, would have grown by nearly three hundred a day.
const keepBackups = 10

// pruneBackups deletes all but the newest keepBackups pre-apply configs. Failure
// is logged and ignored: a full backup dir must never stop a reconcile, which is
// the operation that keeps the tunnel matching the subscriptions.
func (r *Reconciler) pruneBackups() {
	found, err := filepath.Glob(filepath.Join(r.BackupDir, "config.json.*"))
	if err != nil || len(found) <= keepBackups {
		return
	}
	sort.Strings(found)
	for _, old := range found[:len(found)-keepBackups] {
		if err := os.Remove(old); err != nil {
			r.Log.Warn("reconcile: could not prune an old backup", "path", old, "err", err)
		}
	}
}
