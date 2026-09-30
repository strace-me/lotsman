package core

import (
	"context"
	"errors"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/reconcile"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/rulesets"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// ruleSetUpdater returns the UPDATER for a v2 document's data source, or nil when
// there is nothing to fetch.
//
// This is the daemon's updater, not a client-shaped one, and that is the point. The
// client used to provision rule-sets through its own package, which filled in what was
// MISSING and skipped what was present — fine as a first run, and quietly wrong after
// it, because a list that went stale upstream stayed stale on the box forever while
// the router re-read it on the document's refresh period. The shrink guard had the
// same shape of gap: it can only compare a candidate against what is live, and a
// client that never re-fetched a healthy file had nothing to compare.
//
// So one updater, both roots. The client pays for the whole bundle rather than the
// few hundred KB of tags it uses, which is the trade: a few MB per refresh, in
// exchange for a list that is actually current and a guard that is actually armed.
func (c *Core) ruleSetUpdater(rc *reconcile.Reconciler) (*rulesets.Updater, time.Duration, error) {
	var src config.RuleSetSource
	found := false
	for _, s := range config.RuleSetSources(c.opts.V2Config) {
		if s.Fetch && s.Repo != "" {
			src, found = s, true
			break
		}
	}
	if !found {
		// A v1 document declares no data sources. The flags remain the v1 path, and
		// saying so is better than silently doing nothing to a config that looks fed.
		if c.opts.RuleSetRepo == "" {
			return nil, 0, nil
		}
		src = config.RuleSetSource{
			Name:  "flags",
			Repo:  c.opts.RuleSetRepo,
			Pin:   c.opts.RuleSetPin,
			Fetch: true,
		}
	}

	required := requiredRuleSets(c.reg)
	if len(required) == 0 {
		return nil, 0, nil
	}
	every := src.Refresh
	if every <= 0 {
		every = c.opts.RuleSetRefresh
	}
	if every <= 0 {
		every = 6 * time.Hour
	}
	// A pin in the document means "stay on this release"; empty means "track
	// latest". AutoBump=true overrode that, which made the pin decorative: the
	// updater asked GitHub for `releases/latest` every period and failed with a 403
	// (unauthenticated limit is 60/h) for a tag it already had. The flag is there
	// for an operator who wants to keep riding latest while pinning as a floor.
	autoBump := c.opts.RuleSetAutoBump || src.Pin == ""
	up := &rulesets.Updater{
		Repo: src.Repo, Required: required, LiveDir: c.opts.RuleSetDir,
		Pin: src.Pin, AutoBump: autoBump, MinRatio: c.opts.RuleSetMinRatio, PerTag: true,
		DryRun:      c.opts.DryRun,
		SnapshotDir: c.opts.RuleSetSnapshotDir,
		Counter:     rulesets.SingboxCounter{Bin: c.opts.SingboxBin},
		Releaser:    rulesets.GitHubReleaser{},
		PathFor:     singbox.RuleSetRelPath,
		Log:         c.log,
	}
	// The snapshot has to span the swap AND the reconcile that validates it: a release
	// whose rule-sets make `sing-box check` fail is undone, not left in service. The
	// daemon wires exactly this and for the same reason.
	if rc != nil {
		up.OnApplied = func(ctx context.Context) error { return c.reconcileBox(ctx, rc) }
	}
	if up.SnapshotDir == "" {
		up.SnapshotDir = c.opts.RuleSetDir + "/snapshots"
	}
	return up, every, nil
}

// updateRuleSets runs one guarded pass. A refused release is a no-op, not a failure to
// shout about every period: the shrink guard is working by keeping what we have, and
// the updater already logged WHY it refused.
func (c *Core) updateRuleSets(ctx context.Context, up *rulesets.Updater) {
	if err := up.Update(ctx); err != nil && !errors.Is(err, reconcile.ErrDeferred) {
		c.log.Warn("rule-set update failed (keeping the current set)", "err", err)
	}
}

// RefreshRuleSets runs one pass on demand, for a config that just introduced a
// rule-set the disk has never seen. Without it a Reload would hand the reconciler a
// config referencing a missing file, and the reconciler would correctly refuse it and
// fall back to a full re-exec — which drops the tunnel, the exact thing the in-place
// reload exists to avoid.
func (c *Core) RefreshRuleSets(ctx context.Context) {
	up, _, err := c.ruleSetUpdater(nil)
	if err != nil || up == nil {
		return
	}
	c.updateRuleSets(ctx, up)
}

func requiredRuleSets(reg *registry.Registry) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range reg.Services {
		for _, t := range s.RuleSets {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// startRuleSetRefresh runs the updater on the document's period. It replaces the
// old "provision what is missing" call: the first pass still covers a fresh install,
// and every pass after it is a real update.
func (c *Core) startRuleSetRefresh(ctx context.Context, rc *reconcile.Reconciler) {
	up, every, err := c.ruleSetUpdater(rc)
	if err != nil {
		c.log.Warn("rule-set updater not started", "err", err)
		return
	}
	if up == nil {
		return
	}
	// A pass now, before anything reconciles. The ordering is the whole point: a config
	// naming a rule-set the disk does not have yet makes the reconciler's own
	// `sing-box check` fail, and that drops the tunnel. This is the fault the old
	// gap-fill in the reconcile path was papering over, and it belongs here because
	// this is the only thing that fetches.
	c.updateRuleSets(ctx, up)
	c.log.Info("rule-set autoupdate enabled", "repo", up.Repo, "pin", up.Pin,
		"interval", every.String(), "dir", up.LiveDir, "required", len(up.Required),
		"dry_run", c.opts.DryRun)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// A refused release is a no-op, not a failure to shout about every
				// period: the shrink guard is doing its job by keeping what we have,
				// and the updater already logged WHY.
				c.updateRuleSets(ctx, up)
			}
		}
	}()
}
