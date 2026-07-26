package probing

import (
	"log/slog"
	"testing"
	"time"
)

func TestProbeNowIsNonBlockingUnderPressure(t *testing.T) {
	// No Run goroutine drains the trigger, so many more requests than the buffer
	// holds must neither block nor panic — a UI mashing "recheck" cannot wedge the
	// engine. ProbeNow touches only the trigger channel, so the nil deps are fine.
	e := New(nil, nil, nil, nil, nil, nil, nil, time.Second, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			e.ProbeNow("youtube")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ProbeNow blocked — the trigger send is not non-blocking")
	}
}
