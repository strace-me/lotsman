package noderank

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func rankerWithNode(t *testing.T, node string, delayMs int) *Ranker {
	t.Helper()
	api := &fakeAPI{delays: map[string]map[string]int{
		node: {defaultScreenURL: delayMs, "http://svc.invalid": delayMs},
	}}
	return New(api, 1, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A node that answers fast and carries nothing is the exact failure latency
// cannot see, so the measurement has to reach the Quality the ranking scores.
func TestGoodputReachesTheRankedQuality(t *testing.T) {
	r := rankerWithNode(t, "node-a", 40)
	r.Goodput = func(context.Context, string) (float64, bool) { return 512, true }
	q := r.probe(context.Background(), "node-a", "http://svc.invalid")
	if q.Samples == 0 {
		t.Fatal("the fixture node did not answer at all")
	}
	if q.GoodputKBps != 512 {
		t.Errorf("goodput = %v, want the measured 512 — latency alone cannot see a frozen node", q.GoodputKBps)
	}
}

// A refusal — which is what the hook returns while a live session is on the link
// — must leave the ranking untouched rather than record zero throughput, since
// zero would read as "this node carries nothing".
func TestRefusedMeasurementIsNotRecordedAsZero(t *testing.T) {
	r := rankerWithNode(t, "node-a", 40)
	r.Goodput = func(context.Context, string) (float64, bool) { return 0, false }
	if q := r.probe(context.Background(), "node-a", "http://svc.invalid"); q.GoodputKBps != 0 || q.Samples == 0 {
		t.Errorf("refusal produced %+v; it must leave goodput unset, not demote the node", q)
	}
}

// Measuring volume through a node that never answered buys nothing and costs
// bytes on the uplink.
func TestDeadNodeIsNotMeasuredForVolume(t *testing.T) {
	r := rankerWithNode(t, "node-a", 0) // never answers
	called := false
	r.Goodput = func(context.Context, string) (float64, bool) { called = true; return 999, true }
	r.probe(context.Background(), "node-a", "http://svc.invalid")
	if called {
		t.Error("a node that did not answer was still burst-probed")
	}
}

// nil keeps the old behaviour exactly: a router that does not want burst probes
// on its uplink must be able to have none.
func TestGoodputHookIsOptional(t *testing.T) {
	r := rankerWithNode(t, "node-a", 40)
	if r.Goodput != nil {
		t.Fatal("the hook is set by default; it must be opt-in")
	}
	if q := r.probe(context.Background(), "node-a", "http://svc.invalid"); q.GoodputKBps != 0 {
		t.Errorf("goodput = %v with no hook, want 0", q.GoodputKBps)
	}
}
