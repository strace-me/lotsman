package core

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// hostlistRefresh is how often declared domain lists are re-fetched. These lists
// change on the order of days, and each rebuild pulls every source over the network,
// so the subscription cadence would be pure waste.
const hostlistRefresh = time.Hour

// EnsureDomainLists fetches any declared list whose file is MISSING and reports
// whether it wrote one, so the caller can re-parse the config.
//
// The ordering here is load-bearing and easy to get wrong: a service's `domain_lists`
// are merged into its domains when the config is PARSED. Rebuilding after the parse —
// inside Start, say — cannot affect the run it delayed, because the domain set is
// already fixed. So this runs BEFORE the parse that matters, and the caller reloads.
//
// Only missing files are fetched. A list that already exists is left to the background
// loop, so a normal start pays no network latency at all; only a fresh install waits.
// Every failure is logged and swallowed: a dead mirror must not stop the tunnel coming
// up, and the config will simply carry whatever else the service declares.
func EnsureDomainLists(ctx context.Context, conf *config.Config, timeout time.Duration, log *slog.Logger) (rebuilt bool) {
	var missing []config.Hostlist
	for _, hl := range conf.Hostlists {
		if _, err := os.Stat(hl.Out); err != nil {
			missing = append(missing, hl)
		}
	}
	if len(missing) == 0 {
		return false
	}
	log.Info("fetching domain lists that have no file yet", "lists", len(missing))
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	m := aggregate.NewManager(subscription.NewHTTPFetcher())
	for _, hl := range missing {
		if err := aggregate.Rebuild(ctx, m, specOf(hl), false, log); err != nil {
			log.Warn("domain list rebuild failed (the service falls back to what else it declares)", "list", hl.Name, "err", err)
			continue
		}
		rebuilt = true
	}
	return rebuilt
}

// refreshHostlists rebuilds every declared list, best-effort. Used by the background
// loop; failures are logged and swallowed (aggregate.Rebuild already refuses to
// overwrite a good file with a broken fetch).
func (c *Core) refreshHostlists(ctx context.Context, timeout time.Duration) {
	if len(c.conf.Hostlists) == 0 {
		return
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	m := aggregate.NewManager(subscription.NewHTTPFetcher())
	for _, hl := range c.conf.Hostlists {
		if err := aggregate.Rebuild(ctx, m, specOf(hl), false, c.log); err != nil {
			c.log.Warn("hostlist rebuild failed (keeping the existing file)", "list", hl.Name, "err", err)
		}
	}
}

func specOf(hl config.Hostlist) aggregate.RebuildSpec {
	return aggregate.RebuildSpec{
		Name: hl.Name, Out: hl.Out, Sources: hl.Sources,
		Exclude: hl.Exclude, MinKeepRatio: hl.MinKeepRatio,
	}
}

// hostlistLoop keeps the declared domain lists fresh in the background. A rebuild only
// writes the file: the running config keeps the domains it parsed at startup, so a
// changed list takes effect on the next config apply or restart. That is deliberate —
// silently re-routing under a running tunnel would be worse than being a restart behind.
func (c *Core) hostlistLoop(ctx context.Context) {
	t := time.NewTicker(hostlistRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.refreshHostlists(ctx, hostlistRefresh/2)
		}
	}
}
