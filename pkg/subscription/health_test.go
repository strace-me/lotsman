package subscription

import (
	"testing"
	"time"
)

func TestFreshNodeStartsQuarantined(t *testing.T) {
	tr := NewTracker()
	now := time.Now()
	tr.OnSeen("n1", now)

	h, ok := tr.Get("n1")
	if !ok || h.Status != StatusQuarantine {
		t.Fatalf("fresh node status = %v, want quarantine", h.Status)
	}
	if len(tr.ActiveIDs()) != 0 {
		t.Error("fresh node must not be in active set")
	}
	if !tr.DueForRetry("n1", now) {
		t.Error("fresh node should be immediately due for first check")
	}
}

func TestCheckPassActivates(t *testing.T) {
	tr := NewTracker()
	now := time.Now()
	tr.OnSeen("n1", now)
	tr.OnCheck("n1", true, now)

	h, _ := tr.Get("n1")
	if h.Status != StatusActive || h.ConsecutiveFail != 0 {
		t.Fatalf("after pass: status=%v fail=%d, want active/0", h.Status, h.ConsecutiveFail)
	}
	if len(tr.ActiveIDs()) != 1 {
		t.Error("active node missing from active set")
	}
}

func TestQuarantineBackoffThenDead(t *testing.T) {
	tr := NewTracker()
	now := time.Now()
	tr.OnSeen("n1", now)

	// Failure 1 -> quarantine, retry +1h.
	tr.OnCheck("n1", false, now)
	h, _ := tr.Get("n1")
	if h.Status != StatusQuarantine || !h.NextRetryAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("fail1: status=%v next=%v", h.Status, h.NextRetryAt)
	}
	if tr.DueForRetry("n1", now) {
		t.Error("should not be due before +1h")
	}
	if !tr.DueForRetry("n1", now.Add(time.Hour)) {
		t.Error("should be due at +1h")
	}

	// Failures 2,3 -> still quarantine, backoff +4h, +24h.
	tr.OnCheck("n1", false, now.Add(time.Hour))
	h, _ = tr.Get("n1")
	if h.Status != StatusQuarantine {
		t.Fatalf("fail2 status=%v, want quarantine", h.Status)
	}
	tr.OnCheck("n1", false, now.Add(5*time.Hour))
	h, _ = tr.Get("n1")
	if h.Status != StatusQuarantine {
		t.Fatalf("fail3 status=%v, want quarantine", h.Status)
	}

	// Failure 4 (backoff exhausted) -> dead.
	tr.OnCheck("n1", false, now.Add(30*time.Hour))
	h, _ = tr.Get("n1")
	if h.Status != StatusDead {
		t.Fatalf("fail4 status=%v, want dead", h.Status)
	}
	if tr.DueForRetry("n1", now.Add(100*time.Hour)) {
		t.Error("dead node must not be due for retry")
	}
}

func TestRecoveryClearsFailures(t *testing.T) {
	tr := NewTracker()
	now := time.Now()
	tr.OnSeen("n1", now)
	tr.OnCheck("n1", false, now)               // quarantine
	tr.OnCheck("n1", true, now.Add(time.Hour)) // recovers

	h, _ := tr.Get("n1")
	if h.Status != StatusActive || h.ConsecutiveFail != 0 {
		t.Fatalf("recovery: status=%v fail=%d, want active/0", h.Status, h.ConsecutiveFail)
	}
}

func TestMissingGraceThenRemovedThenPruned(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	tr.OnSeen("n1", t0)
	tr.OnCheck("n1", true, t0)

	// Within grace: still active.
	tr.OnMissing("n1", t0.Add(12*time.Hour))
	if h, _ := tr.Get("n1"); h.Status != StatusActive {
		t.Fatalf("within grace status=%v, want active", h.Status)
	}

	// Past 24h grace: removed.
	tRemoved := t0.Add(25 * time.Hour)
	tr.OnMissing("n1", tRemoved)
	if h, _ := tr.Get("n1"); h.Status != StatusRemoved {
		t.Fatalf("past grace status=%v, want removed", h.Status)
	}

	// Not pruned before retention; pruned after 7 days removed.
	if got := tr.Prune(tRemoved.Add(6 * 24 * time.Hour)); len(got) != 0 {
		t.Errorf("pruned too early: %v", got)
	}
	if got := tr.Prune(tRemoved.Add(8 * 24 * time.Hour)); len(got) != 1 || got[0] != "n1" {
		t.Errorf("prune = %v, want [n1]", got)
	}
	if _, ok := tr.Get("n1"); ok {
		t.Error("node should be gone after prune")
	}
}

func TestRemovedNodeRevivesOnReappear(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	tr.OnSeen("n1", t0)
	tr.OnCheck("n1", true, t0)
	tr.OnMissing("n1", t0.Add(25*time.Hour)) // removed

	// Reappears in a later pull -> back to quarantine (must re-prove itself).
	tr.OnSeen("n1", t0.Add(30*time.Hour))
	if h, _ := tr.Get("n1"); h.Status != StatusQuarantine {
		t.Fatalf("revived status=%v, want quarantine", h.Status)
	}
}

func TestDeadNodePrunedAfterRetention(t *testing.T) {
	tr := NewTracker()
	now := time.Now()
	tr.OnSeen("n1", now)
	for i, at := range []time.Duration{0, time.Hour, 5 * time.Hour, 30 * time.Hour} {
		tr.OnCheck("n1", false, now.Add(at))
		_ = i
	}
	if h, _ := tr.Get("n1"); h.Status != StatusDead {
		t.Fatal("setup: node should be dead")
	}
	dead := now.Add(30 * time.Hour)
	if got := tr.Prune(dead.Add(8 * 24 * time.Hour)); len(got) != 1 {
		t.Errorf("dead prune = %v, want [n1]", got)
	}
}
