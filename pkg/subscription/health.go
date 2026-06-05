package subscription

import "time"

// Node health lifecycle (spec 4.2.4 / 4.2.5). A fresh node is quarantined
// until it passes a check, so dead-on-arrival nodes (some providers ship many)
// never pollute the active pools. The state machine here is pure: callers feed
// events and the current time, it returns transitions. Running the actual
// handshake/reachability/caps probe is the caller's job.
type Status string

const (
	StatusQuarantine Status = "quarantine" // newly seen or failing; not in pools
	StatusActive     Status = "active"     // passed a check; eligible for pools
	StatusDead       Status = "dead"       // exhausted retries; awaiting drop
	StatusRemoved    Status = "removed"    // gone from subscription; awaiting drop
)

// retryBackoff is the quarantine re-check schedule. After the first failure a
// node is quarantined and retried at +1h, +4h, +24h; if the last retry also
// fails the node is declared dead.
var retryBackoff = []time.Duration{time.Hour, 4 * time.Hour, 24 * time.Hour}

const (
	deadRetention    = 7 * 24 * time.Hour // dead this long with no recovery -> drop
	removedGrace     = 24 * time.Hour     // missing from sub this long -> removed
	removedRetention = 7 * 24 * time.Hour // removed this long -> drop
)

// Health is one node's lifecycle state. The zero value is not valid; create
// via Tracker.seen.
type Health struct {
	Status          Status
	ConsecutiveFail int
	LastCheck       time.Time
	NextRetryAt     time.Time
	FirstSeen       time.Time
	LastSeenInSub   time.Time
	statusSince     time.Time
}

// Tracker holds health per node ID across subscription pulls and checks.
type Tracker struct {
	health map[string]*Health
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{health: map[string]*Health{}} }

// Get returns a node's health and whether it is tracked.
func (t *Tracker) Get(id string) (Health, bool) {
	h, ok := t.health[id]
	if !ok {
		return Health{}, false
	}
	return *h, true
}

// OnSeen records that a node is present in the latest subscription pull. A
// brand-new node starts quarantined (must pass a check before going active). A
// node previously marked removed but reappearing is revived to quarantine.
func (t *Tracker) OnSeen(id string, now time.Time) {
	h, ok := t.health[id]
	if !ok {
		t.health[id] = &Health{
			Status:        StatusQuarantine,
			FirstSeen:     now,
			LastSeenInSub: now,
			NextRetryAt:   now, // eligible for an immediate first check
			statusSince:   now,
		}
		return
	}
	h.LastSeenInSub = now
	if h.Status == StatusRemoved {
		h.Status = StatusQuarantine
		h.ConsecutiveFail = 0
		h.NextRetryAt = now
		h.statusSince = now
	}
}

// OnMissing records that a node was absent from the latest pull. After the
// grace period it transitions to removed (spec 4.2.5).
func (t *Tracker) OnMissing(id string, now time.Time) {
	h, ok := t.health[id]
	if !ok {
		return
	}
	if h.Status == StatusRemoved {
		return
	}
	if now.Sub(h.LastSeenInSub) >= removedGrace {
		h.Status = StatusRemoved
		h.statusSince = now
	}
}

// OnCheck folds a health-check result into a node's state. A success activates
// it and clears failures. A failure quarantines it with backoff, and once the
// backoff schedule is exhausted marks it dead.
func (t *Tracker) OnCheck(id string, ok bool, now time.Time) {
	h, tracked := t.health[id]
	if !tracked {
		return
	}
	h.LastCheck = now
	if ok {
		if h.Status != StatusActive {
			h.statusSince = now
		}
		h.Status = StatusActive
		h.ConsecutiveFail = 0
		h.NextRetryAt = time.Time{}
		return
	}

	h.ConsecutiveFail++
	if h.ConsecutiveFail > len(retryBackoff) {
		if h.Status != StatusDead {
			h.statusSince = now
		}
		h.Status = StatusDead
		h.NextRetryAt = time.Time{}
		return
	}
	if h.Status != StatusQuarantine {
		h.statusSince = now
	}
	h.Status = StatusQuarantine
	h.NextRetryAt = now.Add(retryBackoff[h.ConsecutiveFail-1])
}

// DueForRetry reports whether a quarantined node is ready for another check.
func (t *Tracker) DueForRetry(id string, now time.Time) bool {
	h, ok := t.health[id]
	if !ok || h.Status != StatusQuarantine {
		return false
	}
	return !now.Before(h.NextRetryAt)
}

// ActiveIDs returns the IDs currently eligible for pools.
func (t *Tracker) ActiveIDs() []string {
	var out []string
	for id, h := range t.health {
		if h.Status == StatusActive {
			out = append(out, id)
		}
	}
	return out
}

// Prune drops nodes that have been dead or removed past their retention window
// and returns the dropped IDs.
func (t *Tracker) Prune(now time.Time) []string {
	var dropped []string
	for id, h := range t.health {
		switch h.Status {
		case StatusDead:
			if now.Sub(h.statusSince) >= deadRetention {
				delete(t.health, id)
				dropped = append(dropped, id)
			}
		case StatusRemoved:
			if now.Sub(h.statusSince) >= removedRetention {
				delete(t.health, id)
				dropped = append(dropped, id)
			}
		}
	}
	return dropped
}
