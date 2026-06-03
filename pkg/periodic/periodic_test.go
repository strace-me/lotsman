package periodic

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunsAtStartAndOnTick(t *testing.T) {
	var n int32
	r := New(slog.New(slog.DiscardHandler))
	r.Add(Task{
		Name: "t", Interval: 15 * time.Millisecond, RunAtStart: true,
		Fn: func(context.Context) error { atomic.AddInt32(&n, 1); return nil },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	r.Run(ctx) // blocks until timeout

	// 1 at start + several ticks.
	if got := atomic.LoadInt32(&n); got < 3 {
		t.Errorf("ran %d times, want >=3 (start + ticks)", got)
	}
}

func TestSkipsZeroIntervalAndNilFn(t *testing.T) {
	r := New(slog.New(slog.DiscardHandler))
	r.Add(Task{Name: "zero", Interval: 0, Fn: func(context.Context) error { return nil }})
	r.Add(Task{Name: "nilfn", Interval: time.Second})
	if len(r.tasks) != 0 {
		t.Errorf("expected both skipped, have %d tasks", len(r.tasks))
	}
}

func TestFailingTaskDoesNotStopRunner(t *testing.T) {
	var ok int32
	r := New(slog.New(slog.DiscardHandler))
	r.Add(Task{Name: "bad", Interval: 10 * time.Millisecond, RunAtStart: true,
		Fn: func(context.Context) error { return context.DeadlineExceeded }})
	r.Add(Task{Name: "good", Interval: 10 * time.Millisecond, RunAtStart: true,
		Fn: func(context.Context) error { atomic.AddInt32(&ok, 1); return nil }})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r.Run(ctx)
	if atomic.LoadInt32(&ok) < 1 {
		t.Error("good task should keep running despite bad task failing")
	}
}
