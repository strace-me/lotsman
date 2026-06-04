package remctl

import (
	"sync"

	"github.com/strace-me/lotsman/pkg/singbox"
)

// ActiveSet tracks the per-service remediations currently applied to the live
// sing-box config and keeps that map CONSISTENT with what the commit (reconcile)
// actually committed (LOT-24). It is the backing store behind the controller's
// Actions: Apply/Rollback mutate the map then commit; if the commit fails, the
// map is reverted to its prior state so it never advertises a remediation the
// live config does not carry (and never drops one the live config still carries).
//
// Without this revert, a failed reconcile after a map write left the entry set,
// so the NEXT reconcile would re-emit a remediation the controller considers
// "not applied" — applying it without a fresh decision. Snapshot is what the
// reconciler reads (rc.Remediations) to fold remediations into generation.
//
// commit is called WITHOUT the lock held (it calls reconcile, which calls
// Snapshot, which takes the lock — holding it would deadlock); the map is
// already updated to the desired state before commit reads it.
type ActiveSet struct {
	mu     sync.Mutex
	active map[string]singbox.Remediation
	commit func() error // push the current map to the live config (reconcile)
}

// NewActiveSet builds an ActiveSet whose Apply/Rollback drive commit (the
// reconcile). commit must be non-nil.
func NewActiveSet(commit func() error) *ActiveSet {
	return &ActiveSet{active: map[string]singbox.Remediation{}, commit: commit}
}

// Snapshot returns a copy of the active remediations, or nil when none are
// active (nil keeps generated output byte-identical to the no-remediation
// config). Safe for concurrent use; wire as the reconciler's Remediations func.
func (a *ActiveSet) Snapshot() map[string]singbox.Remediation {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.active) == 0 {
		return nil
	}
	cp := make(map[string]singbox.Remediation, len(a.active))
	for k, v := range a.active {
		cp[k] = v
	}
	return cp
}

// Apply sets the service's remediation and commits. On commit error it reverts
// the map to its prior state (restoring or removing the entry) and returns the
// error, so the map matches the unchanged live config.
func (a *ActiveSet) Apply(service string, _ int, rem singbox.Remediation) error {
	a.mu.Lock()
	prev, existed := a.active[service]
	a.active[service] = rem
	a.mu.Unlock()

	if err := a.commit(); err != nil {
		a.mu.Lock()
		if existed {
			a.active[service] = prev
		} else {
			delete(a.active, service)
		}
		a.mu.Unlock()
		return err
	}
	return nil
}

// Rollback removes the service's remediation and commits. On commit error it
// restores the entry (the live config may still carry it) and returns the error.
func (a *ActiveSet) Rollback(service string) error {
	a.mu.Lock()
	prev, existed := a.active[service]
	delete(a.active, service)
	a.mu.Unlock()

	if err := a.commit(); err != nil {
		a.mu.Lock()
		if existed {
			a.active[service] = prev
		}
		a.mu.Unlock()
		return err
	}
	return nil
}

// Actions returns the controller Actions backed by this set.
func (a *ActiveSet) Actions() Actions {
	return Actions{Apply: a.Apply, Rollback: a.Rollback}
}
