// Package periodic runs background maintenance tasks on their own intervals:
// checking for Flowseal updates, refreshing subscriptions, rebuilding hostlists,
// pruning stale state. Each task runs in its own goroutine; a failing task logs
// and retries on its next tick rather than taking the others down.
package periodic

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Task is a named periodic job.
type Task struct {
	Name       string
	Interval   time.Duration
	RunAtStart bool // also run once immediately when the runner starts
	Fn         func(context.Context) error
}

// Runner schedules tasks.
type Runner struct {
	tasks []Task
	log   *slog.Logger
}

// New builds a runner.
func New(log *slog.Logger) *Runner { return &Runner{log: log} }

// Add registers a task (before Run). Tasks with Interval <= 0 are skipped.
func (r *Runner) Add(t Task) {
	if t.Interval > 0 && t.Fn != nil {
		r.tasks = append(r.tasks, t)
	}
}

// Run launches every task and blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range r.tasks {
		wg.Add(1)
		go func(t Task) {
			defer wg.Done()
			r.loop(ctx, t)
		}(t)
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context, t Task) {
	if t.RunAtStart {
		r.exec(ctx, t)
	}
	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.exec(ctx, t)
		}
	}
}

func (r *Runner) exec(ctx context.Context, t Task) {
	if err := t.Fn(ctx); err != nil {
		r.log.Warn("periodic task failed", "task", t.Name, "err", err)
	}
}
