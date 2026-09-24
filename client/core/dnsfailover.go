package core

import (
	"context"
	"net"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/dataplane"
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
	remoteList := append([]string(nil), c.conf.DNS.Failover...)       // stable snapshots; the lists rarely change
	directList := append([]string(nil), c.conf.DNS.DirectFailover...) // LOT-83
	if len(remoteList) > 0 && sentinel == "" {
		c.log.Warn("dns: remote failover disabled — no tun peer address to probe the resolver through")
		remoteList = nil
	}
	if len(remoteList) == 0 && len(directList) == 0 {
		return
	}

	interval := c.opts.Interval * 2
	if interval < 20*time.Second {
		interval = 20 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	// Each half keeps its own fail/rotation counters and its own pause deadline, so a
	// dead remote resolver never stalls the direct probe or vice versa.
	var rem dnsFailoverState
	var dir dnsFailoverState
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		if !c.box.Alive(ctx) {
			rem.fails, dir.fails = 0, 0 // a dead box is superviseBox's problem, not a DNS failure
			continue
		}
		if len(remoteList) > 0 {
			c.tickRemoteFailover(ctx, sentinel, remoteList, &rem)
		}
		if len(directList) > 0 {
			c.tickDirectFailover(ctx, directList, &dir)
		}
	}
}

// dnsFailoverState is one resolver's consecutive-failure and rotation counters.
type dnsFailoverState struct {
	fails     int
	rotations int
}

// tickRemoteFailover probes the remote resolver through the tun and rotates it when
// it goes dark. Extracted from the loop so the direct half is not blocked by this
// half's backoff.
func (c *Core) tickRemoteFailover(ctx context.Context, sentinel string, list []string, st *dnsFailoverState) {
	if c.probeResolverViaTun(ctx, sentinel) {
		st.fails, st.rotations = 0, 0
		return
	}
	st.fails++
	if st.fails < dnsFailoverThreshold {
		c.log.Warn("dns: active resolver did not answer", "consecutive", st.fails, "threshold", dnsFailoverThreshold)
		return
	}
	st.fails = 0
	if st.rotations >= len(list) {
		c.log.Warn("dns: cycled every failover provider without recovery — pausing rotation (the tunnel or all resolvers may be down)", "providers", len(list))
		st.rotations = 0
		return
	}
	from, to, conf, err := c.rotateDNSFailover()
	if err != nil {
		c.log.Warn("dns: failover rotation failed", "err", err)
		return
	}
	if conf == nil || from == to {
		return // nothing to rotate to
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
		return
	}
	st.rotations++
	c.log.Info("dns: failed over to another provider", "from", from, "to", to)
}

// tickDirectFailover probes the Direct resolver DIRECTLY (off the tun) and rotates
// it — to a public provider — when the pinned endpoint goes dark. This is the
// LOT-83 half: direct/zapret rungs resolve through this resolver, and a pinned
// office DNS that does not answer on the current network left them unable to
// resolve a target at all.
func (c *Core) tickDirectFailover(ctx context.Context, list []string, st *dnsFailoverState) {
	server := c.directResolverAddress()
	if server == "" {
		return // no addressable Direct resolver (e.g. type: local) to probe
	}
	if c.probeResolverDirect(ctx, server) {
		st.fails, st.rotations = 0, 0
		return
	}
	st.fails++
	if st.fails < dnsFailoverThreshold {
		c.log.Warn("dns: direct resolver did not answer", "server", server, "consecutive", st.fails, "threshold", dnsFailoverThreshold)
		return
	}
	st.fails = 0
	if st.rotations >= len(list) {
		c.log.Warn("dns: cycled every direct failover provider without recovery — pausing rotation", "providers", len(list))
		st.rotations = 0
		return
	}
	from, to, conf, err := c.rotateDNSDirectFailover()
	if err != nil {
		c.log.Warn("dns: direct failover rotation failed", "err", err)
		return
	}
	if conf == nil || from == to {
		return
	}
	if err := c.Reload(conf); err != nil {
		c.log.Error("dns: direct failover could not be applied — still on the old resolver", "from", from, "attempted", to, "err", err)
		if _, _, _, rerr := c.rotateDNSDirectFailoverTo(from); rerr != nil {
			c.log.Warn("dns: could not restore the previous direct provider", "err", rerr)
		}
		return
	}
	st.rotations++
	c.log.Info("dns: direct resolver failed over to another provider", "from", from, "to", to)
}

// directResolverAddress is the Direct resolver's current endpoint, or "" when there
// is none (no direct split, or a type:local server with no address).
func (c *Core) directResolverAddress() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.conf == nil || c.conf.DNS == nil || c.conf.DNS.Direct == "" {
		return ""
	}
	for _, s := range c.conf.DNS.Servers {
		if s.Name == c.conf.DNS.Direct {
			return s.Address
		}
	}
	return ""
}

// probeResolverDirect resolves the canary through the Direct resolver directly,
// off the tun (SO_BINDTODEVICE to the WAN), so it measures the endpoint production
// uses and not the VPN resolver a tun-routed probe would reach.
func (c *Core) probeResolverDirect(ctx context.Context, server string) bool {
	r := dataplane.DirectResolver(server, c.wanIface, 4*time.Second)
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := r.LookupHost(lctx, dnsFailoverCanary)
	return err == nil
}

// rotateDNSDirectFailover advances dns.direct to the next provider and returns the
// config to Reload. Mirrors rotateDNSFailover.
func (c *Core) rotateDNSDirectFailover() (from, to string, conf *config.Config, err error) {
	c.stateMu.Lock()
	cur := ""
	if c.conf.DNS != nil {
		cur = currentDirectProvider(c.conf.DNS)
	}
	next := ""
	if c.conf.DNS != nil {
		next = nextFailoverProvider(cur, c.conf.DNS.DirectFailover)
	}
	c.stateMu.Unlock()
	if c.conf.DNS == nil || next == "" || next == cur {
		return cur, cur, nil, nil
	}
	return c.rotateDNSDirectFailoverTo(next)
}

// rotateDNSDirectFailoverTo pins the Direct resolver to a named provider, or puts
// the previous one back when a reload did not apply.
func (c *Core) rotateDNSDirectFailoverTo(provider string) (from, to string, conf *config.Config, err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.conf.DNS == nil {
		return "", "", nil, nil
	}
	cur := currentDirectProvider(c.conf.DNS)
	if provider == "" || provider == cur {
		return cur, cur, nil, nil
	}
	if err := c.conf.DNS.SetDirectProvider(provider); err != nil {
		return cur, provider, nil, err
	}
	return cur, provider, c.conf, nil
}

// currentDirectProvider is the provider alias the Direct resolver is pinned to
// ("" when it is a manual, non-provider endpoint).
func currentDirectProvider(d *config.DNS) string {
	for _, s := range d.Servers {
		if s.Name == d.Direct {
			return s.Provider
		}
	}
	return ""
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
