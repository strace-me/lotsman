package enginehealth

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// controllable check: healthy flag + restart counter.
type fakeEngine struct {
	healthy  bool
	restarts int
}

func newCheck(name string, e *fakeEngine, threshold int, cooldown time.Duration) *Check {
	return &Check{
		Name:      name,
		Healthy:   func(context.Context) bool { return e.healthy },
		Restart:   func(context.Context) error { e.restarts++; e.healthy = true; return nil }, // restart heals it
		Threshold: threshold,
		Cooldown:  cooldown,
	}
}

func TestWANDownNeverRestarts(t *testing.T) {
	e := &fakeEngine{healthy: false}
	w := &Watchdog{WANUp: func(context.Context) bool { return false }, Checks: []*Check{newCheck("sb", e, 1, 0)}, Log: quietLog()}
	for i := 0; i < 5; i++ {
		w.Run(context.Background())
	}
	if e.restarts != 0 {
		t.Errorf("WAN down must never restart, got %d restarts", e.restarts)
	}
}

func TestRestartsWedgedEngineAfterThreshold(t *testing.T) {
	e := &fakeEngine{healthy: false}
	w := &Watchdog{WANUp: func(context.Context) bool { return true }, Checks: []*Check{newCheck("sb", e, 3, 0)}, Log: quietLog()}
	// Restart's heal would mask the streak, so keep it unhealthy until the restart.
	e.healthy = false
	c := w.Checks[0]
	c.Restart = func(context.Context) error { e.restarts++; return nil } // does NOT heal, to test threshold/cooldown

	w.Run(context.Background()) // fail 1
	w.Run(context.Background()) // fail 2
	if e.restarts != 0 {
		t.Fatalf("must not restart before threshold (3), got %d", e.restarts)
	}
	w.Run(context.Background()) // fail 3 -> restart
	if e.restarts != 1 {
		t.Fatalf("should restart at threshold, got %d", e.restarts)
	}
}

func TestHealthyResetsStreak(t *testing.T) {
	e := &fakeEngine{healthy: false}
	w := &Watchdog{WANUp: func(context.Context) bool { return true }, Now: func() time.Time { return time.Unix(0, 0) }, Log: quietLog()}
	c := newCheck("sb", e, 3, 0)
	c.Restart = func(context.Context) error { e.restarts++; return nil }
	w.Checks = []*Check{c}

	w.Run(context.Background()) // fail 1
	e.healthy = true
	w.Run(context.Background()) // healthy -> streak reset
	e.healthy = false
	w.Run(context.Background()) // fail 1 again
	w.Run(context.Background()) // fail 2
	if e.restarts != 0 {
		t.Errorf("a healthy pass must reset the streak (no restart yet), got %d", e.restarts)
	}
}

func TestCooldownBlocksRestartStorm(t *testing.T) {
	e := &fakeEngine{healthy: false}
	now := time.Unix(1000, 0)
	w := &Watchdog{WANUp: func(context.Context) bool { return true }, Now: func() time.Time { return now }, Log: quietLog()}
	c := newCheck("sb", e, 1, 5*time.Minute)
	c.Restart = func(context.Context) error { e.restarts++; return nil } // stays unhealthy
	w.Checks = []*Check{c}

	w.Run(context.Background()) // fail 1 (threshold 1) -> restart #1
	if e.restarts != 1 {
		t.Fatalf("first restart expected, got %d", e.restarts)
	}
	now = now.Add(2 * time.Minute) // within cooldown
	w.Run(context.Background())
	if e.restarts != 1 {
		t.Errorf("cooldown must block restart, got %d", e.restarts)
	}
	now = now.Add(4 * time.Minute) // cooldown elapsed (6m > 5m)
	w.Run(context.Background())
	if e.restarts != 2 {
		t.Errorf("after cooldown a still-wedged engine restarts again, got %d", e.restarts)
	}
}

func TestRecoveryHealsAndStops(t *testing.T) {
	e := &fakeEngine{healthy: false}
	w := &Watchdog{WANUp: func(context.Context) bool { return true }, Checks: []*Check{newCheck("sb", e, 1, 0)}, Log: quietLog()}
	w.Run(context.Background()) // fail -> restart (newCheck.Restart heals)
	if e.restarts != 1 || !e.healthy {
		t.Fatalf("restart should fire and heal, restarts=%d healthy=%v", e.restarts, e.healthy)
	}
	w.Run(context.Background()) // now healthy -> no further restart
	if e.restarts != 1 {
		t.Errorf("healthy engine must not be restarted again, got %d", e.restarts)
	}
}
