package core

import (
	"context"
	"time"
)

// maxBoxBackoff caps the wait between restart attempts. A config sing-box
// refuses is not fixed by trying harder, so the interval grows, but it never
// grows so far that a transient failure leaves the tunnel down for an hour.
const maxBoxBackoff = 2 * time.Minute

// notReadyGrace is how many consecutive ticks a live-but-unreachable sing-box is
// tolerated before the supervisor restarts it. A box can legitimately take a while to
// bind its control port; restarting on the first miss would be a kill loop on a slow
// host, which is strictly worse than the wedge it is trying to fix.
const notReadyGrace = 3

// hostDNSMissGrace is how many consecutive unanswered probes through the sentinel are
// tolerated before the host's own resolver is handed back.
//
// The case this exists for is the nastiest one on a laptop: the tunnel is up and every
// other health signal is green, but DNS through the sentinel has stopped working (a
// roam moved the ground under it). Nothing else in the system can see that — the box is
// alive, the Clash API answers — so without this the machine sits with no DNS while the
// tray says everything is fine. Handing the real resolver back is the safe direction:
// the worst case is queries leaking to the ISP, which beats no queries at all.
const hostDNSMissGrace = 3

// superviseBox brings sing-box back when it dies.
//
// Without this the client keeps steering a data plane that is gone: the brain
// reasserts selectors against a Clash API that no longer answers, every probe
// fails, and it escalates the whole chain blaming the network — when the actual
// fault is a process that exited. Nothing in the logs would name the real cause.
//
// The restart is deliberately paired with a readiness wait: sing-box binds its
// control port a moment after starting, and driving it before then is what made
// the very first apply fail on every startup.
// watchHostDNS probes the redirected resolver and, after hostDNSMissGrace consecutive
// misses, gives the host its own resolver back. It only runs while the box is alive, so
// a probe failing here means the REDIRECT is broken rather than the tunnel being down —
// the case no other health signal can see.
func (c *Core) watchHostDNS(ctx context.Context, misses *int) {
	if !c.hostDNS.Engaged() {
		*misses = 0
		return
	}
	if err := c.hostDNS.Probe(ctx); err == nil {
		*misses = 0
		return
	}
	*misses++
	c.log.Warn("host-dns: the redirected resolver did not answer while the tunnel is up",
		"misses", *misses, "restore_after", hostDNSMissGrace)
	if *misses < hostDNSMissGrace {
		return
	}
	*misses = 0
	if err := c.hostDNS.Restore(); err != nil {
		c.log.Error("host-dns: could not hand the host resolver back", "err", err)
		c.hostDNSBroken = true
		return
	}
	c.log.Warn("host-dns: handed the host's own resolver back — its DNS was going nowhere; " +
		"the redirect will be re-asserted once it answers again")
}

func (c *Core) superviseBox(ctx context.Context) {
	backoff := c.opts.Interval
	notReady := 0  // consecutive ticks with a live process whose control plane is silent
	dnsMisses := 0 // consecutive host-DNS probes that went unanswered while the box was alive
	t := time.NewTimer(c.opts.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		// Alive() is only "the process exists". A sing-box whose control plane never
		// came up cannot be steered — Start treats that as fatal and refresh.go calls it
		// "indistinguishable from dead" — yet the supervisor, the ONE component that can
		// restart a wedged box, used the weaker test: a restart that half-succeeded then
		// looked healthy on the next tick and was never retried. Require both, but only
		// after a couple of consecutive misses, so a box that is merely slow to bind is
		// not killed and restarted forever.
		if c.box.Alive(ctx) && (c.controlAlive(ctx) || notReady < notReadyGrace) {
			if c.box.Alive(ctx) && !c.controlAlive(ctx) {
				notReady++
				c.log.Warn("sing-box is running but its control plane is not answering", "misses", notReady, "restart_after", notReadyGrace)
			} else {
				notReady = 0
			}
			// The tun is up: re-assert the host-DNS redirect. Idempotent (a no-op when
			// already engaged), so this re-covers a box restart the supervisor did not
			// drive itself — e.g. the reconciler restarting sing-box on a node rotation —
			// making "resolv.conf points at the sentinel iff the tun is up" hold by
			// construction rather than by the down-branch's luck.
			if c.hostDNS != nil && !c.hostDNSBroken {
				if err := c.hostDNS.Redirect(); err != nil {
					c.log.Warn("host-dns: re-redirect on a healthy tick", "err", err)
				}
				c.watchHostDNS(ctx, &dnsMisses)
			}
			backoff = c.opts.Interval
			t.Reset(c.opts.Interval)
			continue
		}

		// The tun is gone with sing-box, so the host's DNS sentinel now points at a
		// dead address. Put the host's real resolver back for the outage, or it has no
		// DNS at all — indefinitely if sing-box refuses to restart (a bad config).
		// Restore is a no-op if already restored, so this is safe to call every tick.
		if c.hostDNS != nil {
			if err := c.hostDNS.Restore(); err != nil {
				c.log.Warn("host-dns: restore during a sing-box outage", "err", err)
			}
		}

		c.log.Warn("sing-box is not running — restarting it", "retry_in", backoff)
		if err := c.box.Restart(ctx); err != nil {
			c.log.Error("could not restart sing-box", "err", err, "next_try", backoff)
		} else if err := c.waitControlReady(ctx, 15*time.Second); err != nil {
			c.log.Error("sing-box restarted but its control plane never came up", "err", err)
		} else {
			// The brain reasserts selectors every interval, so the data plane
			// re-converges on its own from here.
			c.log.Info("sing-box restarted")
			notReady = 0
			// The tun is back — redirect the host's DNS into it again (no-op if still
			// engaged). Not re-verified: the mechanism was proven at first start.
			if c.hostDNS != nil && !c.hostDNSBroken {
				if err := c.hostDNS.Redirect(); err != nil {
					c.log.Warn("host-dns: re-redirect after a restart", "err", err)
				}
			}
			backoff = c.opts.Interval
			t.Reset(c.opts.Interval)
			continue
		}

		if backoff < maxBoxBackoff {
			backoff *= 2
		}
		t.Reset(backoff)
	}
}
