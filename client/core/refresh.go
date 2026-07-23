package core

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/reconcile"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// boxRunner adapts pkg/reconcile's command Runner to a client that owns the
// sing-box process instead of an init service.
//
// The reconciler issues exactly two commands: the `check` that validates a
// candidate config, and the restart that puts it into force. The check is a
// genuine subprocess, the same one the router runs. Anything else is the
// restart, which for a client means asking the ProxyCore to re-read the file the
// reconciler has already swapped into place — deliberately NOT rewriting it,
// since the reconciler is the one holding the backup it may need to roll back to.
type boxRunner struct {
	box ProxyCore
}

func (r boxRunner) Run(ctx context.Context, name string, args ...string) error {
	if len(args) > 0 && args[0] == "check" {
		if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s check: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return r.box.Restart(ctx)
}

// newReconciler builds the config reconciler that keeps the running sing-box in
// step with the subscriptions. Without it the client renders its config once at
// startup and never notices a node rotating out from under it — an ordinary
// event, not an edge case.
//
// This is the daemon's reconciler verbatim, so the client inherits its safety
// rails: it never applies a config that fails `sing-box check`, it skips a tick
// whose fetch came back degraded (a flaky mirror returning fewer nodes must not
// churn the config), it backs up before swapping, and it rolls back if sing-box
// does not come back alive.
func (c *Core) newReconciler() *reconcile.Reconciler {
	services := make([]registry.Service, 0, len(c.reg.Services))
	for _, s := range c.reg.Services {
		services = append(services, s)
	}
	rc := &reconcile.Reconciler{
		Services:   services,
		Devices:    c.conf.Devices,
		Opts:       c.singboxOptions(),
		Subs:       c.conf.Subscriptions,
		Loader:     subscription.NewManager(subscription.NewHTTPFetcher()),
		Pools:      c.conf.Pools,
		ConfigPath: c.opts.SingboxConfig,
		Runner:     boxRunner{box: c.box},
		SingboxBin: c.opts.SingboxBin,
		// Any non-check command reaches ProxyCore.Restart; the value is a label for
		// logs, not a program to execute.
		RestartCmd: []string{"proxycore-restart"},
		Alive:      c.controlAlive,
		Log:        c.log,
	}
	// Persist the anti-churn baseline so the degraded-fetch guard fires on the very
	// first reconcile after a restart instead of resetting to zero and applying a
	// degraded startup fetch wholesale (LOT-29/LOT-45). Without a store the guard is
	// inert until the first clean apply of THIS process — the exact window it exists
	// to protect. The daemon seeds it identically.
	if c.opts.BaselineFile != "" {
		rc.Baseline = reconcile.NewBaselineStore(c.opts.BaselineFile)
		if n := rc.Baseline.Load(); n > 0 {
			rc.SetBaseline(n)
			c.log.Info("reconcile: baseline restored", "path", c.opts.BaselineFile, "last_nodes", n)
		}
	}
	return rc
}

// controlAlive reports whether sing-box came back after a restart. It asks the
// Clash API rather than the process table: a live process whose control plane
// never came up cannot be steered, which is indistinguishable from dead for our
// purposes.
func (c *Core) controlAlive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if _, err := c.clash.Proxy(ctx, "direct"); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// refreshLoop re-reconciles on an interval until ctx is cancelled. A failure is
// logged and retried on the next tick: a subscription mirror being briefly
// unreachable is normal and must not stop the loop.
func (c *Core) refreshLoop(ctx context.Context, rc *reconcile.Reconciler, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := rc.Reconcile(ctx); err != nil {
				c.log.Warn("subscription refresh failed (retrying next tick)", "err", err)
			}
		}
	}
}
