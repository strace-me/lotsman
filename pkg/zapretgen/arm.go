package zapretgen

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// applyArmed installs a covered, changed composition as the single writer of the
// nfqws strategy, then canaries it (LOT-10b-arm):
//
//  1. baseline-probe the affected services (which are reachable BEFORE the switch),
//  2. write composed.sh, symlink it active, restart nfqws,
//  3. after a settle delay, re-probe — a service that WAS reachable but no longer
//     is counts as degraded,
//  4. degraded => record the applied recipes as failures (so the KB demotes them
//     and the next compose picks different recipes) and roll back to the
//     last-known-good target; clean => record successes and commit LKG = composed.
//
// A restart failure during the switch rolls back the same way. Returns nil on a
// handled canary rollback (it is an expected outcome, not an error) so the loop
// continues; a real I/O failure (write) is returned.
func (r *Reconciler) applyArmed(ctx context.Context, active []registry.Service, plan zaptune.Plan, launcher string) error {
	a := r.arm

	baseline := make(map[string]bool, len(active))
	for _, svc := range active {
		if svc.ProbeTarget != "" {
			baseline[svc.Name] = a.Probe(ctx, svc.ProbeTarget)
		}
	}

	prev := r.lkgTarget
	if err := r.writeSwitch(ctx, launcher); err != nil {
		r.rollback(ctx, prev)
		return fmt.Errorf("zapret-arm: apply failed, rolled back to %q: %w", prev, err)
	}

	if a.Settle > 0 {
		select {
		case <-time.After(a.Settle):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var degraded []string
	for _, svc := range active {
		// Only blame the switch for a service that WAS reachable and now is not. A
		// service with no probe target, or already broken pre-switch, is not a canary
		// signal.
		if svc.ProbeTarget == "" || !baseline[svc.Name] {
			continue
		}
		if !r.canaryProbe(ctx, svc.ProbeTarget) {
			degraded = append(degraded, svc.Name)
		}
	}

	if len(degraded) > 0 {
		r.recordAll(active, plan, false)
		r.rollback(ctx, prev)
		r.log.Warn("zapret-arm: canary FAILED — rolled back to last-known-good",
			"degraded", degraded, "lkg", prev, "chosen", plan.Chosen)
		return nil
	}

	r.recordAll(active, plan, true)
	r.lkgTarget = a.ComposedPath
	r.log.Info("zapret-arm: composed nfqws config applied + canary passed",
		"services", serviceNames(active), "chosen", plan.Chosen, "path", a.ComposedPath)
	return nil
}

// writeSwitch writes the composed launcher, symlinks it active, and restarts the
// engine. The file is written 0755 (it is an executable launcher the init runs).
func (r *Reconciler) writeSwitch(ctx context.Context, launcher string) error {
	if err := os.WriteFile(r.arm.ComposedPath, []byte(launcher), 0o755); err != nil {
		return fmt.Errorf("write composed launcher: %w", err)
	}
	if err := r.arm.Runner.Run(ctx, "ln", "-sfn", r.arm.ComposedPath, r.arm.ActiveLink); err != nil {
		return fmt.Errorf("symlink active: %w", err)
	}
	return r.restart(ctx)
}

// rollback points the active symlink back at target and restarts. Best-effort:
// failures are logged loudly (the engine may be in a bad state), not returned —
// the caller is already handling the original failure.
func (r *Reconciler) rollback(ctx context.Context, target string) {
	if target == "" {
		r.log.Error("zapret-arm: no last-known-good target to roll back to")
		return
	}
	if err := r.arm.Runner.Run(ctx, "ln", "-sfn", target, r.arm.ActiveLink); err != nil {
		r.log.Error("zapret-arm: ROLLBACK symlink failed", "target", target, "err", err)
		return
	}
	if err := r.restart(ctx); err != nil {
		r.log.Error("zapret-arm: ROLLBACK restart failed", "target", target, "err", err)
	}
}

func (r *Reconciler) restart(ctx context.Context) error {
	if len(r.arm.RestartCmd) == 0 {
		return fmt.Errorf("no restart command configured")
	}
	return r.arm.Runner.Run(ctx, r.arm.RestartCmd[0], r.arm.RestartCmd[1:]...)
}

// canaryProbe probes target up to CanaryProbes times; ok if ANY attempt succeeds
// (a single reachable probe means the path works).
func (r *Reconciler) canaryProbe(ctx context.Context, target string) bool {
	n := r.arm.CanaryProbes
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		if r.arm.Probe(ctx, target) {
			return true
		}
	}
	return false
}

// recordAll folds the canary outcome into the KB for every applied recipe, so a
// failed composition demotes its recipes (next compose differs) and a passing one
// promotes them. No-op when no Record hook is wired.
func (r *Reconciler) recordAll(active []registry.Service, plan zaptune.Plan, ok bool) {
	if r.arm.Record == nil {
		return
	}
	for _, svc := range active {
		if id := plan.Chosen[svc.Name]; id != "" {
			r.arm.Record(svc.Name, id, ok)
		}
	}
}
