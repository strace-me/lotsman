package core

import (
	"context"
	"time"
)

// maxBoxBackoff caps the wait between restart attempts. A config sing-box
// refuses is not fixed by trying harder, so the interval grows, but it never
// grows so far that a transient failure leaves the tunnel down for an hour.
const maxBoxBackoff = 2 * time.Minute

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
	t := time.NewTimer(c.opts.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		if c.box.Alive(ctx) {
			backoff = c.opts.Interval
			t.Reset(c.opts.Interval)
			continue
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
