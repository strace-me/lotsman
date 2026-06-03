// Package correlate distinguishes a systemic outage (ISP down, DNS poisoned,
// WAN dead — many services failing at once) from a local problem (one service's
// strategy stopped working). During a systemic outage, churning per-service
// strategies is pointless and harmful: it burns through the fallback chain and
// thrashes the data plane while the real cause is upstream. Lotsman consults
// this before escalating, and holds when the failure is systemic.
package correlate

import "sync"

// Detector tracks the latest health of each service and reports whether the
// failure pattern looks systemic.
type Detector struct {
	mu           sync.Mutex
	healthy      map[string]bool
	minServices  int     // need at least this many tracked before judging
	downFraction float64 // >= this fraction down => systemic
}

// New builds a detector. A systemic verdict requires at least minServices
// tracked and at least downFraction of them currently failing.
func New(minServices int, downFraction float64) *Detector {
	if minServices < 1 {
		minServices = 1
	}
	return &Detector{healthy: map[string]bool{}, minServices: minServices, downFraction: downFraction}
}

// Set records a service's latest health.
func (d *Detector) Set(service string, healthy bool) {
	d.mu.Lock()
	d.healthy[service] = healthy
	d.mu.Unlock()
}

// Summary returns how many tracked services are down and the total tracked.
func (d *Detector) Summary() (down, total int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	total = len(d.healthy)
	for _, ok := range d.healthy {
		if !ok {
			down++
		}
	}
	return down, total
}

// Systemic reports whether enough services are simultaneously down to suspect
// an upstream cause rather than a per-service strategy failure.
func (d *Detector) Systemic() bool {
	down, total := d.Summary()
	if total < d.minServices {
		return false
	}
	return float64(down)/float64(total) >= d.downFraction
}
