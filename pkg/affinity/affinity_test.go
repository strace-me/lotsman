package affinity

import (
	"testing"
	"time"
)

func TestStickyWithinTTL(t *testing.T) {
	s := New(10 * time.Minute)
	now := time.Unix(1_700_000_000, 0)

	picks := 0
	pick := func() string { picks++; return "node-A" }

	// First flow picks; subsequent flows within TTL reuse without re-picking.
	if got := s.Resolve("client|discord", now, pick); got != "node-A" {
		t.Fatalf("first = %q", got)
	}
	for i := 0; i < 5; i++ {
		if got := s.Resolve("client|discord", now.Add(time.Duration(i)*time.Minute), func() string { return "node-B" }); got != "node-A" {
			t.Errorf("flow %d moved to %q, want sticky node-A", i, got)
		}
	}
	if picks != 1 {
		t.Errorf("pick called %d times, want 1 (balancer runs once per session)", picks)
	}
}

func TestRepicksAfterExpiry(t *testing.T) {
	s := New(10 * time.Minute)
	now := time.Unix(1_700_000_000, 0)
	s.Resolve("k", now, func() string { return "A" })

	got := s.Resolve("k", now.Add(11*time.Minute), func() string { return "B" })
	if got != "B" {
		t.Errorf("after TTL expiry = %q, want re-pick B", got)
	}
}

func TestForgetAndPrune(t *testing.T) {
	s := New(time.Minute)
	now := time.Unix(1_700_000_000, 0)
	s.Resolve("k1", now, func() string { return "A" })
	s.Resolve("k2", now, func() string { return "A" })

	s.Forget("k1")
	if _, ok := s.Get("k1", now); ok {
		t.Error("k1 should be forgotten")
	}

	s.Prune(now.Add(2 * time.Minute)) // both expired
	if _, ok := s.Get("k2", now.Add(2*time.Minute)); ok {
		t.Error("k2 should be pruned")
	}
}

func TestPerHouseholdVsPerClientKeys(t *testing.T) {
	s := New(time.Hour)
	now := time.Unix(1_700_000_000, 0)
	// Two clients, per-client scope -> independent assignments.
	a := s.Resolve("client|192.168.1.50|discord", now, func() string { return "A" })
	b := s.Resolve("client|192.168.1.60|discord", now, func() string { return "B" })
	if a == b {
		t.Errorf("per-client keys should allow different outbounds, both got %q", a)
	}
	// Household scope -> one shared assignment.
	h1 := s.Resolve("household|discord", now, func() string { return "H" })
	h2 := s.Resolve("household|discord", now, func() string { return "OTHER" })
	if h1 != h2 {
		t.Errorf("household key should be shared: %q vs %q", h1, h2)
	}
}
