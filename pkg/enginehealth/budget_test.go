package enginehealth

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The defect, as a test. An engine that CANNOT start — a payload file is missing, so
// the start is not flaky, it is impossible — was restarted 559 times over three days,
// every ten seconds, with the fail counter reset after each attempt. The streak never
// grew, so nothing ever concluded. A watchdog that cannot conclude is a metronome.
func TestImpossibleEngineIsGivenUpOnNotRestartedForever(t *testing.T) {
	restarts := 0
	c := &Check{
		Name: "desync", Threshold: 1, Budget: 3,
		Healthy: func(context.Context) bool { return false },
		Restart: func(context.Context) error { restarts++; return nil },
	}
	w := &Watchdog{
		WANUp:  func(context.Context) bool { return true },
		Checks: []*Check{c},
		Now:    func() time.Time { return time.Now() },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for i := 0; i < 20; i++ {
		if err := w.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if restarts > 3 {
		t.Errorf("restarted %d times against a budget of 3", restarts)
	}
}

// Recovery has to clear the whole slate, or an engine that flakes once, refuses
// forever, and comes back is left permanently condemned.
func TestRecoveryClearsTheBudget(t *testing.T) {
	up := false
	restarts := 0
	c := &Check{
		Name: "desync", Threshold: 1, Budget: 2,
		Healthy: func(context.Context) bool { return up },
		Restart: func(context.Context) error { restarts++; return nil },
	}
	w := &Watchdog{
		WANUp: func(context.Context) bool { return true }, Checks: []*Check{c},
		Now: func() time.Time { return time.Now() },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for i := 0; i < 2; i++ {
		_ = w.Run(context.Background())
	}
	up = true
	_ = w.Run(context.Background())
	up = false
	_ = w.Run(context.Background())
	_ = w.Run(context.Background())
	if restarts < 3 {
		t.Errorf("restarts = %d; after recovery the engine must be able to fail again", restarts)
	}
}

// Giving up has to be LOUD, because the whole point is that silence is what let an
// impossible engine sit there for three days.
func TestGivingUpIsLoggedAtError(t *testing.T) {
	var buf strings.Builder
	c := &Check{
		Name: "desync", Threshold: 1, Budget: 1,
		Healthy: func(context.Context) bool { return false },
		Restart: func(context.Context) error { return nil },
	}
	w := &Watchdog{
		WANUp: func(context.Context) bool { return true }, Checks: []*Check{c},
		Now: func() time.Time { return time.Now() },
		Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	for i := 0; i < 4; i++ {
		_ = w.Run(context.Background())
	}
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("giving up was not an error; log was: %s", out)
	}
	if !strings.Contains(out, "cannot start") {
		t.Errorf("the reason was not stated: %s", out)
	}
}
