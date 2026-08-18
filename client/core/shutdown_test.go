package core

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/registry"
)

// stoppableBox is a ProxyCore that only has to answer Stop — the embedded nil
// interface panics on anything else, which is the point: it proves Stop touches
// nothing it should not.
type stoppableBox struct{ ProxyCore }

func (stoppableBox) Stop(context.Context) error { return nil }

func emptyConf() *config.Config {
	return &config.Config{Registry: &registry.Registry{Services: map[string]registry.Service{}}}
}

// A runner that never returns must not cost us the cleanup. Before LOT-70 this
// waited forever, systemd's 90s timer fired, and SIGKILL took the process with the
// audit log unflushed, the sandbox table still diverting packets and the candidate
// nfqws still holding the queue.
func TestStopDoesNotWaitForeverOnAWedgedRunner(t *testing.T) {
	var log bytes.Buffer
	c := New(emptyConf(), stoppableBox{}, Options{}, slog.New(slog.NewTextHandler(&log, nil)))

	wedged := make(chan struct{})
	defer close(wedged)
	c.wg.Add(1)
	c.inflight.Store("wedged-loop", struct{}{})
	go func() { defer c.wg.Done(); <-wedged }()

	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	took := time.Since(start)

	if budget := shutdownRunners + shutdownTeardown; took > budget {
		t.Errorf("Stop took %v, over the %v budget — systemd would SIGKILL before the cleanup ran", took, budget)
	}
	// Naming the culprit is half the fix: the next occurrence must be a log line,
	// not another investigation.
	if got := log.String(); !strings.Contains(got, "wedged-loop") {
		t.Errorf("the overrun did not name the runner that caused it; log was:\n%s", got)
	}
}

// The ordinary case must not pay the budget: a clean stop returns as soon as the
// runners do, not after the timeout.
func TestStopReturnsImmediatelyWhenRunnersObeyTheCancel(t *testing.T) {
	var log bytes.Buffer
	c := New(emptyConf(), stoppableBox{}, Options{}, slog.New(slog.NewTextHandler(&log, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.wg.Add(1)
	c.inflight.Store("obedient-loop", struct{}{})
	go func() { defer c.wg.Done(); defer c.inflight.Delete("obedient-loop"); <-ctx.Done() }()

	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if took := time.Since(start); took > shutdownRunners {
		t.Errorf("a clean stop took %v — it waited on the budget instead of on the runners", took)
	}
	if got := log.String(); strings.Contains(got, "ignored the cancel") {
		t.Errorf("a clean stop reported an overrun:\n%s", got)
	}
}

func TestWaitBoundedReportsWhetherItFinished(t *testing.T) {
	var done sync.WaitGroup
	if !waitBounded(&done, 50*time.Millisecond) {
		t.Error("an empty wait group should report finished")
	}

	var stuck sync.WaitGroup
	stuck.Add(1)
	defer stuck.Done()
	start := time.Now()
	if waitBounded(&stuck, 50*time.Millisecond) {
		t.Error("a wait group nobody finishes should report NOT finished")
	}
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Errorf("returned after %v, before the deadline it was given", took)
	}
}
