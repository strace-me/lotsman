// Package damper suppresses strategy flapping. If a service keeps switching
// strategies in a short window (escalate, recover, escalate, ...), each switch
// restarts data-plane processes and disrupts traffic for no benefit. The damper
// counts recent transitions and returns an exponentially growing backoff to add
// to the settling window, so a flapping service is forced to sit still longer
// and longer until it stabilizes.
package damper

import (
	"sync"
	"time"
)

// Damper tracks recent transitions per service.
type Damper struct {
	mu       sync.Mutex
	window   time.Duration // transitions within this window count toward flapping
	maxFree  int           // this many transitions per window incur no backoff
	base     time.Duration // backoff for the first excess transition
	max      time.Duration // backoff ceiling
	events   map[string][]time.Time
}

// New builds a damper. base doubles per excess transition beyond maxFree, up to
// max.
func New(window time.Duration, maxFree int, base, max time.Duration) *Damper {
	return &Damper{
		window: window, maxFree: maxFree, base: base, max: max,
		events: map[string][]time.Time{},
	}
}

// Record logs a transition for a service.
func (d *Damper) Record(service string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events[service] = append(d.pruneLocked(service, now), now)
}

// Count returns how many transitions happened within the window (for adaptive
// thresholds that demand more proof from a flapping service).
func (d *Damper) Count(service string, now time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pruneLocked(service, now))
}

// Backoff returns the extra settling time to apply given recent flapping: zero
// while transitions stay within maxFree, then base, 2*base, 4*base, ... capped
// at max.
func (d *Damper) Backoff(service string, now time.Time) time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.pruneLocked(service, now))
	excess := n - d.maxFree
	if excess <= 0 {
		return 0
	}
	b := d.base
	for i := 1; i < excess; i++ {
		b *= 2
		if b >= d.max {
			return d.max
		}
	}
	if b > d.max {
		b = d.max
	}
	return b
}

// pruneLocked drops events older than the window and returns the survivors,
// also storing them back. Caller holds the lock.
func (d *Damper) pruneLocked(service string, now time.Time) []time.Time {
	cutoff := now.Add(-d.window)
	src := d.events[service]
	kept := src[:0]
	for _, t := range src {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	d.events[service] = kept
	return kept
}
