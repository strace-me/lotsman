package core

import (
	"context"
	"net"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
)

// dnsFailoverCanary is resolved through the tunnel to tell whether the active remote
// resolver still answers. A neutral, always-resolvable, never-censored name so a
// failure means "the resolver is dark", not "this domain is blocked".
const dnsFailoverCanary = "example.com"

// dnsFailoverThreshold is how many consecutive probe failures trip a rotation. High
// enough that a transient blip does not flap the DNS provider.
const dnsFailoverThreshold = 3

// dnsFailoverLoop probes the active remote resolver through the tun and, when it goes
// dark for dnsFailoverThreshold consecutive checks, re-points dns.final at the next
// provider in dns.failover and applies it. sing-box has no native per-rule DNS
// fallback, so the client drives it.
//
// Apply MUST go through Core.Reload (which rebuilds the loop and its reconciler with
// fresh options): mutating the config and running a one-off reconcile instead would be
// reverted on the refresh loop's next tick, because that reconciler snapshots its
// options at build time. Reload waits on the autonomy loop's wg, so this loop lives
// outside it (see Core.failoverWg) or the Reload it calls would deadlock on itself.
//
// It never rotates while sing-box is down (that is superviseBox's job, not a DNS
// fault), and after cycling the whole failover list without a healthy probe in
// between it backs off — a probe that fails for every provider means the tunnel or the
// probe path is down, not the resolvers, and thrashing through them would not help.
func (c *Core) dnsFailoverLoop(ctx context.Context) {
	sentinel := tunSentinel(c.tunOptions())
	if sentinel == "" {
		c.log.Warn("dns: failover disabled — no tun peer address to probe the resolver through")
		return
	}
	list := append([]string(nil), c.conf.DNS.Failover...) // stable snapshot; the list rarely changes

	interval := c.opts.Interval * 2
	if interval < 20*time.Second {
		interval = 20 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	fails, rotations := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		if !c.box.Alive(ctx) {
			fails = 0 // a dead box is superviseBox's problem, not a DNS failure
			continue
		}
		if c.probeResolverViaTun(ctx, sentinel) {
			fails, rotations = 0, 0
			continue
		}
		fails++
		if fails < dnsFailoverThreshold {
			c.log.Warn("dns: active resolver did not answer", "consecutive", fails, "threshold", dnsFailoverThreshold)
			continue
		}
		fails = 0

		if rotations >= len(list) {
			c.log.Warn("dns: cycled every failover provider without recovery — pausing rotation (the tunnel or all resolvers may be down)", "providers", len(list))
			rotations = 0
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Minute):
			}
			continue
		}

		from, to, conf, err := c.rotateDNSFailover()
		if err != nil {
			c.log.Warn("dns: failover rotation failed", "err", err)
			continue
		}
		if conf == nil || from == to {
			continue // nothing to rotate to
		}
		// Only a reload that actually APPLIED means we are now on `to`. Counting the
		// attempt instead would let a run of skipped reloads (a degraded subscription
		// fetch during the very outage that triggered us) walk the in-memory provider
		// through the whole list while sing-box still runs the first one — and then
		// conclude "every provider failed" about providers it never installed.
		if err := c.Reload(conf); err != nil {
			c.log.Error("dns: failover could not be applied — still on the old resolver", "from", from, "attempted", to, "err", err)
			// Put the config back so memory matches what sing-box is actually running.
			if _, _, _, rerr := c.rotateDNSFailoverTo(from); rerr != nil {
				c.log.Warn("dns: could not restore the previous provider in the config", "err", rerr)
			}
			continue
		}
		rotations++
		c.log.Info("dns: failed over to another provider", "from", from, "to", to)
	}
}

// probeResolverViaTun resolves the canary through the tun peer, which hijack-dns routes
// into sing-box's dns block and so to the active `final` resolver. A lookup that answers
// means the active provider is working; a timeout means it has gone dark.
func (c *Core) probeResolverViaTun(ctx context.Context, sentinel string) bool {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "udp", net.JoinHostPort(sentinel, "53"))
		},
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := r.LookupHost(lctx, dnsFailoverCanary)
	return err == nil
}

// rotateDNSFailover advances dns.final to the next provider in the failover list and
// returns the config to Reload. The mutation is under stateMu so a concurrent reconcile
// never reads a half-updated server. Returns from==to (no reload) when there is nothing
// to rotate to.
func (c *Core) rotateDNSFailover() (from, to string, conf *config.Config, err error) {
	c.stateMu.Lock()
	cur := ""
	if c.conf.DNS != nil {
		cur = currentFinalProvider(c.conf.DNS)
	}
	next := ""
	if c.conf.DNS != nil {
		next = nextFailoverProvider(cur, c.conf.DNS.Failover)
	}
	c.stateMu.Unlock()
	if c.conf.DNS == nil || next == "" || next == cur {
		return cur, cur, nil, nil
	}
	return c.rotateDNSFailoverTo(next)
}

// rotateDNSFailoverTo pins the Final resolver to a named provider. Split out so the
// loop can put the config BACK when the reload it attempted did not apply — leaving
// memory on a provider the running sing-box never received is what turns one failed
// apply into a walk through the whole list without installing any of it.
func (c *Core) rotateDNSFailoverTo(provider string) (from, to string, conf *config.Config, err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.conf.DNS == nil {
		return "", "", nil, nil
	}
	cur := currentFinalProvider(c.conf.DNS)
	if provider == "" || provider == cur {
		return cur, cur, nil, nil
	}
	if err := c.conf.DNS.SetFinalProvider(provider); err != nil {
		return cur, provider, nil, err
	}
	return cur, provider, c.conf, nil
}

// currentFinalProvider is the provider alias the Final resolver is currently pinned to.
func currentFinalProvider(d *config.DNS) string {
	for _, s := range d.Servers {
		if s.Name == d.Final {
			return s.Provider
		}
	}
	return ""
}

// nextFailoverProvider returns the provider after cur in list, wrapping around; if cur
// is not in the list it starts at the front. "" only when the list is empty.
func nextFailoverProvider(cur string, list []string) string {
	if len(list) == 0 {
		return ""
	}
	for i, p := range list {
		if p == cur {
			return list[(i+1)%len(list)]
		}
	}
	return list[0]
}
