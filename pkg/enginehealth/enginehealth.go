// Package enginehealth is the data-plane engine watchdog (LOT-33): it detects a
// WEDGED engine (sing-box that can't route, nfqws that isn't desyncing) and
// restarts it — the self-healing answer to the cold-boot race, where engines come
// up before the WAN uplink and latch a broken state ("no route to internet",
// nfqws mis-initialised) that never clears on its own.
//
// The crux is the WAN gate: an engine that can't reach the internet is only worth
// restarting if the BOX itself has internet. If the uplink is genuinely down, the
// engines are not to blame — restarting them would be a pointless restart storm
// while we wait for the link. So the watchdog restarts an unhealthy engine ONLY
// when the WAN is up. This also makes boot ORDER irrelevant: whatever started
// before the WAN gets perturbed back to health once the link is up, without any
// procd ordering hacks.
//
// All probes and restarts are injected, so the policy is pure and unit-testable;
// the caller wires the real WAN probe (box-direct reachability), the per-engine
// health probe (clash direct-delay for sing-box; a DPI'd-target canary for nfqws)
// and the restart command.
package enginehealth

import (
	"context"
	"log/slog"
	"time"
)

// Check is one engine's health gate + recovery. Healthy probes the engine in its
// current state; Restart recovers it. After Threshold consecutive UNHEALTHY
// passes (while the WAN is up) Restart fires, then Cooldown blocks further
// restarts so a still-broken engine is not restart-stormed.
type Check struct {
	Name      string
	Healthy   func(context.Context) bool
	Restart   func(context.Context) error
	Threshold int           // consecutive unhealthy passes before restarting (min 1)
	Cooldown  time.Duration // min time between restarts of this engine

	fails       int
	lastRestart time.Time
}

// Watchdog runs the engine checks behind a WAN gate. Construct the struct and call
// Run on a ticker. Not safe for concurrent use (drive from one goroutine).
type Watchdog struct {
	WANUp  func(context.Context) bool // box itself has internet (gate)
	Checks []*Check
	Now    func() time.Time // injectable clock (nil = time.Now)
	Log    *slog.Logger
}

// Run executes one watchdog pass. If the WAN is down it resets the counters and
// returns (the engines aren't to blame). Otherwise each unhealthy engine advances
// its fail streak and, past its threshold (and out of cooldown), is restarted.
func (w *Watchdog) Run(ctx context.Context) error {
	if !w.WANUp(ctx) {
		for _, c := range w.Checks {
			c.fails = 0
		}
		w.Log.Info("engine-health: WAN down — skipping engine checks (engines not to blame)")
		return nil
	}
	now := w.clock()
	for _, c := range w.Checks {
		if c.Healthy(ctx) {
			if c.fails > 0 {
				w.Log.Info("engine-health: engine recovered", "engine", c.Name)
			}
			c.fails = 0
			continue
		}
		c.fails++
		th := c.Threshold
		if th < 1 {
			th = 1
		}
		w.Log.Warn("engine-health: engine unhealthy while WAN up", "engine", c.Name, "fails", c.fails, "threshold", th)
		if c.fails < th {
			continue
		}
		if !c.lastRestart.IsZero() && now.Sub(c.lastRestart) < c.Cooldown {
			w.Log.Warn("engine-health: engine wedged but in restart cooldown", "engine", c.Name,
				"since_restart", now.Sub(c.lastRestart).String(), "cooldown", c.Cooldown.String())
			continue
		}
		w.Log.Warn("engine-health: restarting WEDGED engine (WAN up, engine unhealthy past threshold)", "engine", c.Name)
		if err := c.Restart(ctx); err != nil {
			w.Log.Error("engine-health: restart failed", "engine", c.Name, "err", err)
			continue
		}
		c.lastRestart = now
		c.fails = 0
	}
	return nil
}

func (w *Watchdog) clock() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
