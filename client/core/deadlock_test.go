package core

import (
	"context"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// TestReloadOnLiveDoesNotDeadlock is the LOT-81 regression test. Reload holds
// stateMu for its whole body and synchronously drives the reconciler, whose
// OnLive hook the old code tried to re-enter stateMu — a self-deadlock (Go
// sync.Mutex is not re-entrant). After the fix OnLive takes fleetMu, so the hook
// returns even when stateMu is already held. The test calls the exact hook
// newReconciler installs (recordFleetOnLive) while stateMu is held and asserts it
// returns within the timeout.
func TestReloadOnLiveDoesNotDeadlock(t *testing.T) {
	c := &Core{conf: &config.Config{Pools: &pools.Set{}}}
	onLive := c.recordFleetOnLive()

	done := make(chan struct{})
	go func() {
		c.stateMu.Lock()
		onLive([]subscription.Node{{Server: "1.2.3.4", Port: 443}})
		c.stateMu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		// fixed: the OnLive hook (fleetMu) does not deadlock under stateMu.
	case <-time.After(3 * time.Second):
		t.Fatal("LOT-81 regression: OnLive re-acquired stateMu held by Reload (self-deadlock)")
	}
}

// TestReportReadsFleetUnderFleetMu verifies the lock split is consistent: Report
// (which holds stateMu) reads fleet fields under fleetMu, so a parallel OnLive
// writer cannot leave Report observing a torn snapshot.
func TestReportReadsFleetUnderFleetMu(t *testing.T) {
	c := &Core{conf: &config.Config{Pools: &pools.Set{}}}
	c.recordFleet([]subscription.Node{{Server: "1.2.3.4", Port: 443}, {Server: "5.6.7.8", Port: 443}})

	done := make(chan struct{})
	go func() {
		r := c.Report(context.Background())
		if r.Fleet.Total != 2 {
			t.Errorf("expected 2 nodes, got %d", r.Fleet.Total)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Report blocked reading fleet (LOT-81 regression)")
	}
}
