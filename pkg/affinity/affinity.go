// Package affinity gives sticky outbound assignments so load balancing does not
// break multi-connection sessions. Without it, url-test/balancer could move a
// client to a different node mid-call or mid-match, resetting its connections.
// With it, the first flow of a (client, service) session picks an outbound and
// subsequent flows reuse it until a TTL expires — Discord stays on one relay,
// a game stays on one node.
//
// Scope (per-client vs per-household) is expressed by how the caller builds the
// key: "client|192.168.1.50|discord" vs "household|discord". Pure logic; time
// is injected.
package affinity

import (
	"sync"
	"time"
)

type entry struct {
	outbound string
	expires  time.Time
}

// Store holds sticky assignments with a TTL.
type Store struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]entry
}

// New builds a store with the given stickiness TTL.
func New(ttl time.Duration) *Store {
	return &Store{ttl: ttl, entries: map[string]entry{}}
}

// Resolve returns the sticky outbound for key, assigning one via pick() if there
// is no live assignment. The chosen outbound is (re)stamped with a fresh TTL.
// pick is only called on a miss, so the balancer runs once per session, not per
// flow.
func (s *Store) Resolve(key string, now time.Time, pick func() string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && now.Before(e.expires) {
		return e.outbound
	}
	out := pick()
	s.entries[key] = entry{outbound: out, expires: now.Add(s.ttl)}
	return out
}

// Get returns the live sticky outbound for key, if any.
func (s *Store) Get(key string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && now.Before(e.expires) {
		return e.outbound, true
	}
	return "", false
}

// Forget drops a key's stickiness (e.g. tear_down_on_pool_change for a pool that
// went away).
func (s *Store) Forget(key string) {
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
}

// Prune removes expired entries.
func (s *Store) Prune(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if !now.Before(e.expires) {
			delete(s.entries, k)
		}
	}
}
