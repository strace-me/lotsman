package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// The fleet a machine has is a fact it already learned. Re-learning it on every
// boot means asking the network for permission to keep working, and on a laptop
// the network is not there yet: on 2026-08-13 the client started before the host
// had a resolver, both HTTP subscriptions failed with `lookup ... on [::1]:53`,
// and it built a config from the three INLINE nodes — the ones that need no fetch.
// The owner came home to a fleet of three.
//
// The anti-churn guard already knew: `reconcile: degraded fetch, retrying
// attempt=1 nodes=3 last=108`. It refused to apply the collapse — but it guards
// the REFRESH path, and the startup generate has no equivalent. Same defect shape
// as the daemon/client divergence: a protection that exists on one path reads as
// if it covered both.
const (
	// fleetRetries is how many times a startup fetch is retried before the snapshot
	// is used. The failure it covers is a boot race measured in seconds, not a dead
	// provider, so the retries are quick and few.
	fleetRetries = 3
	fleetBackoff = 4 * time.Second
)

// fleetSnapshot is the last node set that was fetched cleanly.
type fleetSnapshot struct {
	SavedAt time.Time           `json:"saved_at"`
	Nodes   []subscription.Node `json:"nodes"`
}

// fleetPath is where the snapshot lives — beside the state file, which is the
// other thing this machine knows that a fresh process would otherwise forget.
func (c *Core) fleetPath() string {
	if c.opts.StateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(c.opts.StateFile), "fleet.json")
}

// saveFleet records a node set worth falling back to. Called only for a fetch
// that looks complete; saving a degraded one would teach the machine the very
// collapse this exists to prevent.
func (c *Core) saveFleet(nodes []subscription.Node) {
	p := c.fleetPath()
	if p == "" || len(nodes) == 0 {
		return
	}
	b, err := json.Marshal(fleetSnapshot{SavedAt: time.Now(), Nodes: nodes})
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	// 0600 and root-only: these carry node credentials, exactly like the config and
	// the generated sing-box file next to them.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		c.log.Warn("could not save the fleet snapshot", "path", p, "err", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		c.log.Warn("could not replace the fleet snapshot", "path", p, "err", err)
	}
}

// loadFleet returns the last cleanly-fetched node set, or nil.
func (c *Core) loadFleet() []subscription.Node {
	p := c.fleetPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var snap fleetSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		c.log.Warn("the fleet snapshot is unreadable; ignoring it", "path", p, "err", err)
		return nil
	}
	return snap.Nodes
}

// loadNodes fetches the subscriptions for a STARTUP generate, retrying a few
// times and falling back to the last good snapshot rather than to nothing.
//
// "Fewer than we had" is the test, not "zero": the collapse that hurt was 108 → 3,
// and three is not zero. Inline node declarations need no fetch and always
// survive, so a total wipe is not even the failure mode — a plausible-looking
// remnant is, which is worse, because it builds a config that runs.
func (c *Core) loadNodes(ctx context.Context) ([]subscription.Node, *subscription.Manager) {
	prev := c.loadFleet()
	var mgr *subscription.Manager
	var nodes []subscription.Node
	for attempt := 1; ; attempt++ {
		// Direct: this runs at startup, BEFORE our sing-box socks inbound exists, so
		// a proxy fetch here is a guaranteed "connection refused". The refresh loop
		// (box up) is the one that pulls through the tunnel; a failed startup fetch
		// falls back to the last-good snapshot below.
		mgr = subscription.NewManager(subscription.NewHTTPFetcher())
		var errs []error
		nodes, errs = mgr.Load(ctx, c.conf.Subscriptions)
		for _, e := range errs {
			c.log.Warn("subscription load issue", "err", e)
		}
		if len(errs) == 0 || attempt >= fleetRetries {
			break
		}
		c.log.Warn("subscription fetch failed at startup, retrying",
			"attempt", attempt, "of", fleetRetries, "nodes", len(nodes), "errs", len(errs))
		select {
		case <-ctx.Done():
			return nodes, mgr
		case <-time.After(fleetBackoff):
		}
	}

	if len(prev) > len(nodes) {
		c.log.Warn("startup fetch came back short — using the fleet this machine already knew",
			"fetched", len(nodes), "remembered", len(prev),
			"note", "the tunnel keeps working; the next successful refresh replaces this")
		return prev, mgr
	}
	c.saveFleet(nodes)
	return nodes, mgr
}
