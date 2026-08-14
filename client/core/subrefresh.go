package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/strace-me/lotsman/pkg/reconcile"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// SubRefresh is what one subscription yielded when it was asked again: how many
// nodes it produced, and — the part that matters — why it produced none.
//
// The reason this reports per subscription rather than a single ok/failed: on
// 2026-08-12 a freshly added subscription contributed zero nodes and there was no
// way to ask why. The answer was in a log line that had already rotated out of a
// six-minute journald ring, and the owner spent an hour believing the fetch had
// failed when it had actually succeeded and a stale counter was lying (LOT-66).
type SubRefresh struct {
	Name  string `json:"name"`
	Nodes int    `json:"nodes"`
	Err   string `json:"err,omitempty"`
}

// RefreshSubscriptions re-fetches subscriptions and applies the result.
//
// name selects one; empty means all of them. Each declaration is fetched on its
// own so the answer can name the one that failed — a merged fetch reports "2
// errors" and leaves the operator to guess which two.
//
// The apply afterwards is the ordinary reconcile, with every safety rail it
// carries: it validates before swapping, refuses a degraded node set, backs up,
// and rolls back if sing-box does not come back. It deliberately does NOT go
// through Reload, which rebuilds the whole autonomy loop and hits LOT-56 (the
// desync executor is not rebuilt), and which is why "re-fetch now" has until now
// meant "re-save the config and hope".
func (c *Core) RefreshSubscriptions(ctx context.Context, name string) ([]SubRefresh, error) {
	c.mu.Lock()
	conf := c.conf
	c.mu.Unlock()
	if conf == nil {
		return nil, errors.New("no config loaded")
	}

	var want []subscription.Declaration
	for _, d := range conf.Subscriptions {
		if name == "" || d.Name == name {
			want = append(want, d)
		}
	}
	if len(want) == 0 {
		if name != "" {
			return nil, fmt.Errorf("no subscription named %q", name)
		}
		return nil, errors.New("this config declares no subscriptions")
	}

	out := make([]SubRefresh, 0, len(want))
	for _, d := range want {
		mgr := subscription.NewManager(subscription.NewHTTPFetcher())
		nodes, errs := mgr.Load(ctx, []subscription.Declaration{d})
		r := SubRefresh{Name: d.Name, Nodes: len(nodes)}
		for _, e := range errs {
			if r.Err != "" {
				r.Err += "; "
			}
			r.Err += e.Error()
		}
		if r.Err != "" {
			c.log.Warn("subscription refresh failed", "subscription", d.Name, "err", r.Err)
		} else {
			c.log.Info("subscription refreshed", "subscription", d.Name, "nodes", r.Nodes)
		}
		out = append(out, r)
	}

	// Apply whatever the fetch produced. ErrNotApplied and ErrDeferred are not
	// failures of the refresh: the first means the reconciler judged the result
	// degraded and kept what was running — which is the guard working, and the
	// caller has the per-subscription numbers above to see why — and the second
	// means a live voice call is holding the restart off.
	switch err := c.reconcileBox(ctx, c.newReconciler()); {
	case err == nil:
		c.log.Info("subscriptions refreshed and applied", "subscriptions", len(out))
	case errors.Is(err, reconcile.ErrNotApplied):
		c.log.Warn("subscriptions refreshed, but the result was not applied — the node set came back degraded and the running config was kept")
	case errors.Is(err, reconcile.ErrDeferred):
		c.log.Info("subscriptions refreshed; applying deferred while a live voice/RTC flow is in progress")
	default:
		return out, fmt.Errorf("refreshed, but applying failed: %w", err)
	}
	return out, nil
}
