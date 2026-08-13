package core

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/enginehealth"
)

// The check that matters is not "is the engine running" but "is it running when a
// rule expects it". Most days most rules sit on VPN and the composer stops the
// engine deliberately (LOT-46); calling that unhealthy would restart it forever.
func TestDesyncCheckIgnoresAnIdleEngineAndActsOnAWedgedOne(t *testing.T) {
	var alive, occupied bool
	restarts := 0
	healthy := func(context.Context) bool {
		if !occupied {
			return true
		}
		return alive
	}
	wd := &enginehealth.Watchdog{
		WANUp: func(context.Context) bool { return true },
		Checks: []*enginehealth.Check{{
			Name: "desync", Healthy: healthy,
			Restart:   func(context.Context) error { restarts++; return nil },
			Threshold: desyncHealthThreshold,
		}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	run := func(n int) {
		for i := 0; i < n; i++ {
			if err := wd.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Nobody on the rung: the engine is off on purpose, however many passes go by.
	occupied, alive = false, false
	run(desyncHealthThreshold * 3)
	if restarts != 0 {
		t.Fatalf("an idle engine was restarted %d times", restarts)
	}

	// A rule arrives on the rung and the engine is not there.
	occupied = true
	run(desyncHealthThreshold - 1)
	if restarts != 0 {
		t.Errorf("restarted after %d bad passes, threshold is %d", desyncHealthThreshold-1, desyncHealthThreshold)
	}
	run(1)
	if restarts != 1 {
		t.Errorf("a wedged engine under an occupied rung was restarted %d times, want 1", restarts)
	}
}
