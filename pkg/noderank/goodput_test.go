package noderank

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/strace-me/lotsman/pkg/balancer"
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

// The defect LOT-67 is named for: the daemon's measurement IGNORED its own `node`
// argument and pulled bytes through whatever path the probe proxy took, so every
// candidate of a pass received the SAME number. An identical value cannot rank
// anything — the feature was on, logged, and inert.
//
// Live cost, 2026-08-12: youtube and github broke TLS through Extra-1f89ac while
// the active probe reported ok=true rtt=103ms, because it asks generate_204 for a
// body-less answer. Restarting the service "fixed" it by picking a different node
// — a coin toss.
func TestEachCandidateIsMeasuredAboutItself(t *testing.T) {
	api := newFake("frozen", []string{"vpn_url_test", "frozen", "carrying", "direct"},
		map[string]map[string]int{
			// The frozen exit answers FASTER, which is exactly why latency crowns it.
			"frozen":   {defaultScreenURL: 40, svcURL: 40},
			"carrying": {defaultScreenURL: 300, svcURL: 300},
		})
	r := New(api, 1, true, quietLog())

	var mu sync.Mutex
	asked := map[string]int{}
	r.Goodput = func(_ context.Context, node string) (float64, bool) {
		mu.Lock()
		asked[node]++
		mu.Unlock()
		switch node {
		case "frozen":
			return 0.5, true // answers in 40ms, moves nothing: the TSPU volume freeze
		case "carrying":
			return 900, true
		}
		return 0, false
	}

	if _, err := r.Pick(context.Background(), Service{
		Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL,
		Weights: balancer.ProfileFor("streaming"),
	}, []Candidate{{Tag: "frozen"}, {Tag: "carrying"}}); err != nil {
		t.Fatalf("Pick: %v", err)
	}

	for _, node := range []string{"frozen", "carrying"} {
		if asked[node] == 0 {
			t.Errorf("the measurement was never asked about %q — it cannot be per-node", node)
		}
	}

	// And the values must land distinctly on the two nodes' recorded health.
	var frozen, carrying float64
	for _, h := range r.HealthSnapshot() {
		switch h.Node {
		case "frozen":
			frozen = h.GoodputKBps
		case "carrying":
			carrying = h.GoodputKBps
		}
	}
	if frozen == carrying {
		t.Fatalf("both exits recorded %v KiB/s — this is the LOT-67 defect: one number for every candidate", frozen)
	}
	if !(carrying > frozen) {
		t.Errorf("carrying=%v frozen=%v — backwards", carrying, frozen)
	}
}
