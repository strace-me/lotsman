package core

import (
	"context"
	"time"

	"github.com/strace-me/lotsman/pkg/enginehealth"
)

// desyncHealthInterval is how often the desync engine is checked. Short, because
// the window it covers is "a rule is routed direct expecting nfqws to treat its
// traffic, and nfqws is not there" — during which the service is not merely
// unprotected but actively broken, since the rung was chosen on the assumption
// the engine exists.
const desyncHealthInterval = 30 * time.Second

const (
	desyncHealthThreshold = 3               // consecutive bad passes before acting
	desyncHealthCooldown  = 5 * time.Minute // floor between restarts, as on the daemon
)

// engineHealthLoop watches the DESYNC engine and brings it back when it is wedged.
//
// Only the desync engine. The daemon's watchdog also checks sing-box, and porting
// that half would have put a second supervisor on a process that already has a
// better one: superviseBox requires both a live process AND an answering control
// plane, with a grace counter for a box that is merely slow to bind, where the
// daemon's check is a single delay probe. Two components restarting one process on
// different criteria is how a restart storm starts.
//
// The gap this closes is that c.zap.Alive() was read in exactly one place —
// status.go, to paint a dot. Nothing acted on it. A rule sitting on a zapret rung
// with a dead engine is worse than one on VPN: it was routed direct precisely
// because something was supposed to treat its packets, and nothing is.
func (c *Core) engineHealthLoop(ctx context.Context) {
	wd := &enginehealth.Watchdog{
		// Gate: if sing-box is down, superviseBox is already restarting it and the
		// desync engine's state says nothing about the network. Restarting engines
		// under a dead data plane is the storm the WAN gate exists to prevent.
		WANUp: func(hc context.Context) bool { return c.box.Alive(hc) },
		Checks: []*enginehealth.Check{{
			Name: "desync",
			// An engine that is NOT running is only a fault when a rule expects it.
			// With no service on a zapret rung the composer stops it deliberately
			// (LOT-46), and calling that unhealthy would restart it forever on a
			// machine whose rules all sit on VPN — which is most of them, most days.
			Healthy: func(context.Context) bool {
				if c.zap == nil || !c.desyncRungOccupied() {
					return true
				}
				return c.zap.Alive()
			},
			// Recompose from the rules that are on the rung right now rather than
			// re-applying a remembered argv: the membership may have changed while the
			// engine was down, and Reconcile is the one path that already knows how to
			// derive the whole strategy — including stopping the engine when the rung
			// has emptied under us.
			Restart: func(hc context.Context) error {
				if c.zapExec == nil {
					return nil
				}
				return c.zapExec.Reconcile(hc)
			},
			Threshold: desyncHealthThreshold,
			Cooldown:  desyncHealthCooldown,
		}},
		Log: c.log,
	}

	t := time.NewTicker(desyncHealthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := wd.Run(ctx); err != nil {
				c.log.Warn("engine-health pass failed", "err", err)
			}
		}
	}
}

// desyncRungOccupied reports whether any service is currently on a zapret rung,
// i.e. whether the desync engine is supposed to be running at all.
func (c *Core) desyncRungOccupied() bool {
	if c.zapExec == nil {
		return false
	}
	return len(c.zapExec.active()) > 0
}
