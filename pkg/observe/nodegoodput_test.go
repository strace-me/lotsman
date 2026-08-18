package observe

import (
	"testing"
	"time"
)

// The defect LOT-67 names: the throughput half handed every candidate of a pass
// the SAME number — the goodput of whatever path the probe proxy took — and an
// identical value cannot rank anything. Two exits that carried different amounts
// must read differently.
func TestTwoExitsInOnePassGetDifferentNumbers(t *testing.T) {
	window := 10 * time.Second
	snap := Snapshot{Nodes: map[string]NodeCarry{
		// Answers fast, moves nothing that matters: the TSPU volume freeze.
		"Extra-1f89ac": {Bytes: 8 << 10, Flows: 3},
		"Works-aa11":   {Bytes: 4 << 20, Flows: 5},
	}}

	frozen, ok := snap.NodeGoodputKBps("Extra-1f89ac", window)
	if !ok {
		t.Fatal("an exit that carried bytes must produce a verdict")
	}
	working, ok := snap.NodeGoodputKBps("Works-aa11", window)
	if !ok {
		t.Fatal("an exit that carried bytes must produce a verdict")
	}
	if frozen == working {
		t.Fatalf("both exits read %v KiB/s — the measurement is not per-node, which is the whole defect", frozen)
	}
	if !(working > frozen) {
		t.Errorf("the exit that carried 4 MiB reads %v KiB/s, the one that carried 8 KiB reads %v — backwards", working, frozen)
	}
	if want := float64(4<<20) / 1024 / 10; working != want {
		t.Errorf("goodput = %v KiB/s, want %v", working, want)
	}
}

// An exit nobody used gets NO verdict, not a zero. A zero would rank an idle exit
// below a busy one and is exactly the misreading LOT-52 cost nine minutes of false
// verdicts to learn.
func TestAnUnusedExitGetsNoVerdictRatherThanZero(t *testing.T) {
	snap := Snapshot{Nodes: map[string]NodeCarry{"busy": {Bytes: 1 << 20, Flows: 2}}}

	if _, ok := snap.NodeGoodputKBps("never-used", time.Minute); ok {
		t.Error("an exit with no traffic must not produce a verdict")
	}
	// Flows but no bytes is NOT the same question — it is the freeze signature —
	// but it still yields no goodput verdict here; telling those apart needs Flows.
	snap.Nodes["opened-but-stuck"] = NodeCarry{Bytes: 0, Flows: 4}
	if _, ok := snap.NodeGoodputKBps("opened-but-stuck", time.Minute); ok {
		t.Error("zero bytes must not read as a measured zero")
	}
}

func TestNoWindowMeansNoVerdict(t *testing.T) {
	snap := Snapshot{Nodes: map[string]NodeCarry{"n": {Bytes: 1 << 20}}}
	if _, ok := snap.NodeGoodputKBps("n", 0); ok {
		t.Error("dividing by a zero window must not produce a number")
	}
}
