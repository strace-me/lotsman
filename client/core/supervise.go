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
func (c *Core) superviseBox(ctx context.Context) {
	backoff := c.opts.Interval
	notReady := 0 // consecutive ticks with a live process whose control plane is silent
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
