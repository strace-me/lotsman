package core

import (
	"context"
	"time"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// hostlistRefresh is how often declared domain lists are re-fetched. These lists
// change on the order of days, and each rebuild pulls every source over the network,
// so the subscription cadence would be pure waste.
const hostlistRefresh = time.Hour

// hostlistStartTimeout bounds the blocking rebuild done before the config is used, so
// an unreachable list source delays startup by seconds rather than hanging the tunnel.
const hostlistStartTimeout = 30 * time.Second

// refreshHostlists rebuilds every declared domain list once, best-effort.
//
// It is called BEFORE the config's domains are consumed, because a service's
// `domain_lists` are merged into its domains when the config is PARSED — so a list
// file that does not exist yet (a fresh install) would leave that service with only
// its inline domains for the whole run. Rebuilding first means the very first
// generated config already routes and desyncs the full pack.
//
// Every failure is logged and swallowed: a dead list mirror must not stop the tunnel
// from coming up, and aggregate.Rebuild already refuses to overwrite a good file with
// a broken fetch.
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
		spec := aggregate.RebuildSpec{
			Name: hl.Name, Out: hl.Out, Sources: hl.Sources,
			Exclude: hl.Exclude, MinKeepRatio: hl.MinKeepRatio,
		}
		if err := aggregate.Rebuild(ctx, m, spec, false, c.log); err != nil {
			c.log.Warn("hostlist rebuild failed (keeping the existing file)", "list", hl.Name, "err", err)
		}
	}
}

// hostlistLoop keeps the declared domain lists fresh in the background. A rebuild
// writes the file; the running config keeps the domains it parsed at startup, so a
// changed list takes effect on the next config apply or restart — which is why the
// blocking refresh in Start exists for the first-run case.
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
