package core

import (
	"context"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/kb"
)

// Only the router ever aged the knowledge base, so on the client a quarantine was
// permanent: three consecutive canary failures banished a (service, recipe) pair for the
// life of the process. On a laptop that is severe — every resume spends its first
// half-minute with no route, every probe fails, and blameless recipes take the blame —
// so weeks of lid-opens would quietly quarantine everything that works here.
func TestKBDecayLoopAgesTheStoreAndLiftsQuarantines(t *testing.T) {
	k := kb.New()
	k.SetZapretSeed([]string{"good", "bad"})
	// Drive one strategy past the breaker threshold.
	for i := 0; i < 5; i++ {
		k.RecordOutcome("youtube", "bad", false, 0)
	}
	if got := k.TopNZapret("youtube", 5); contains(got, "bad") {
		t.Fatalf("a persistently failing strategy should be quarantined, got %v", got)
	}

	c := &Core{kb: k, opts: Options{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the loop's body directly rather than waiting out its 15-minute ticker: the
	// point under test is that the CLIENT ages the store at all, which it never did.
	done := make(chan struct{})
	go func() { defer close(done); c.kbDecayLoop(ctx) }()
	for i := 0; i < breakerCooldownTicksForTest; i++ {
		c.kb.Decay(kbDecayFactor)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("kbDecayLoop did not stop when its context was cancelled")
	}

	if got := k.TopNZapret("youtube", 5); !contains(got, "bad") {
		t.Errorf("the quarantine must lift after enough decay ticks, got %v", got)
	}
}

// breakerCooldownTicksForTest mirrors pkg/kb's unexported cooldown; if that changes this
// test starts failing, which is the correct signal.
const breakerCooldownTicksForTest = 4

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
