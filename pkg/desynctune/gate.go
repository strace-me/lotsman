package desynctune

import (
	"sync"
	"time"
)

// Gate decides when a tuner pass may start.
//
// Three limits, each for a different way an unguarded search hurts. The sandbox
// is a SINGLETON — one table, one queue, one engine — so two passes at once
// would have them measure through each other. A per-service cooldown stops one
// stubborn service from consuming every window. And a global spacing keeps the
// uplink from being a permanent test rig: a pass applies and probes dozens of
// arms serially, which is minutes of the link working for us instead of for the
// household.
//
// The stamp is taken on RELEASE, and regardless of outcome. Stamping on success
// only would let a service whose search keeps failing retry without pause, which
// is the worst case rather than the one to hurry.
type Gate struct {
	PerService time.Duration // min interval between passes for one service; 0 = 6h
	Global     time.Duration // min interval between ANY two passes; 0 = 15m
	Now        func() time.Time

	mu      sync.Mutex
	running bool
	last    map[string]time.Time
	lastAny time.Time
}

// Begin reports whether a pass for service may start now. When it may, the
// caller must invoke the returned release exactly once — deferring it is the
// intended use, so a panicking pass still frees the sandbox.
func (g *Gate) Begin(service string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.running {
		return nil, false
	}
	if !g.lastAny.IsZero() && now.Sub(g.lastAny) < g.global() {
		return nil, false
	}
	if t, seen := g.last[service]; seen && now.Sub(t) < g.perService() {
		return nil, false
	}
	g.running = true
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			done := g.now()
			g.running = false
			g.lastAny = done
			if g.last == nil {
				g.last = map[string]time.Time{}
			}
			g.last[service] = done
		})
	}, true
}

// Running reports whether a pass holds the sandbox right now.
func (g *Gate) Running() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gate) perService() time.Duration {
	if g.PerService > 0 {
		return g.PerService
	}
	return 6 * time.Hour
}

func (g *Gate) global() time.Duration {
	if g.Global > 0 {
		return g.Global
	}
	return 15 * time.Minute
}
