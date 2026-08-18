package core

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strace-me/lotsman/client/platform/netid"
)

func waitCore(detect func(context.Context) netid.Network) *Core {
	return &Core{log: slog.New(slog.NewTextHandler(io.Discard, nil)), detect: detect}
}

// The failure this closes: a laptop boots faster than its Wi-Fi associates, and
// everything downstream reads "no network" as a fact about the world rather than
// as "not yet" — the KB keys on `default`, the tun's route excludes are computed
// for no subnets, and the desync rung comes up with wan="". On 2026-08-18 those
// excludes swallowed the office resolvers and the machine could not fetch well
// enough to be allowed to repair itself.
func TestStartWaitsForTheNetworkAndThenUsesIt(t *testing.T) {
	var calls atomic.Int32
	c := waitCore(func(context.Context) netid.Network {
		if calls.Add(1) < 3 {
			return netid.Network{Id: netid.Fallback}
		}
		return netid.Network{Id: "office", IFace: "wlan0", Gateway: "10.0.0.1"}
	})
	got := c.waitForNetwork(context.Background(), 30*time.Second)
	if got.Id != "office" {
		t.Errorf("settled on %q, want the network that appeared", got.Id)
	}
	if calls.Load() < 3 {
		t.Errorf("returned after %d probes — it did not actually wait", calls.Load())
	}
}

// Offline is a legitimate state. The client must still come up, or the control
// socket never appears and no UI can say why.
func TestAMachineWithNoNetworkStartsAnyway(t *testing.T) {
	c := waitCore(func(context.Context) netid.Network { return netid.Network{Id: netid.Fallback} })
	start := time.Now()
	got := c.waitForNetwork(context.Background(), 150*time.Millisecond)
	if got.Id != netid.Fallback {
		t.Errorf("got %q, want the fallback", got.Id)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("waited %v past its own deadline", elapsed)
	}
}

// A shutdown during the wait must not be held for the whole deadline — LOT-70 is
// exactly this class of bug, where a startup wait outlives the stop request.
func TestTheWaitReleasesOnCancellation(t *testing.T) {
	c := waitCore(func(context.Context) netid.Network { return netid.Network{Id: netid.Fallback} })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	c.waitForNetwork(ctx, time.Hour)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancellation took %v to release the wait", elapsed)
	}
}
