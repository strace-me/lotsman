package pools

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

func byID(spec ...string) []subscription.Node {
	out := make([]subscription.Node, 0, len(spec))
	for _, s := range spec {
		out = append(out, subscription.Node{ID: s})
	}
	return out
}

func nodeIDs(list []subscription.Node) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, n.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The trap this type exists for: the fastest node and the lowest-latency node are
// usually different, and if latency gets to choose, ten rules pile onto one exit
// (LOT-69, measured: 108 nodes, one exit, ~330 KiB/s shared).
func TestTopByThroughputPicksTheFastestNotTheFirst(t *testing.T) {
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop}
	all := byID("fastest", "slow", "middling")
	goodput := map[string]float64{"fastest": 900, "slow": 10, "middling": 300}

	got, report := p.TopByThroughput(all, goodput, 2)
	if want := []string{"fastest", "middling"}; !equal(nodeIDs(got), want) {
		t.Errorf("members = %v, want %v", nodeIDs(got), want)
	}
	if report.Kept != 2 || report.Ranked != 3 || report.Unmeasured != 0 {
		t.Errorf("report = %+v", report)
	}
}

func TestTopByThroughputIsDeterministicOnTies(t *testing.T) {
	// A float ordering that depended on map iteration would make the generated
	// sing-box config churn, and this project has already paid for a
	// restart-per-diff incident (LOT-1). Identical measurements must give an
	// identical pool, every time.
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop}
	// Six tied nodes, a limit of THREE: the limit has to sit below the admitted
	// count or this takes the early "nothing to narrow" pass and never sorts,
	// which would make the ordering test vacuous.
	all := byID("c", "a", "b", "d", "f", "e")
	goodput := map[string]float64{"a": 500, "b": 500, "c": 500, "d": 500, "e": 500, "f": 500}

	first, _ := p.TopByThroughput(all, goodput, 3)
	for i := 0; i < 20; i++ {
		again, _ := p.TopByThroughput(all, goodput, 3)
		if !equal(nodeIDs(again), nodeIDs(first)) {
			t.Fatalf("run %d differed: %v vs %v", i, nodeIDs(again), nodeIDs(first))
		}
	}
	if want := []string{"a", "b", "c"}; !equal(nodeIDs(first), want) {
		t.Errorf("ties did not break by id: %v, want %v", nodeIDs(first), want)
	}
}

func TestTopByThroughputDoesNotRankOnAnAbsentMeasurement(t *testing.T) {
	// An unmeasured node is not a slow node, but it is not a fast one either.
	// Ranking it needs a number nobody has, and guessing one is how a dead exit
	// wins a throughput race. So it is left out of the ranking AND counted, so
	// the caller can say so out loud.
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop}
	all := byID("measured", "unknown", "zeroed")
	goodput := map[string]float64{"measured": 800, "zeroed": 0}

	// The limit must be BELOW the admitted count, or this takes the early
	// "nothing to narrow" path and never reaches the ranking at all.
	got, report := p.TopByThroughput(all, goodput, 2)
	if len(got) != 1 || got[0].ID != "measured" {
		t.Errorf("members = %v", nodeIDs(got))
	}
	if report.Unmeasured != 2 {
		t.Errorf("unmeasured = %d, want 2", report.Unmeasured)
	}
	if report.Ranked != 1 {
		t.Errorf("ranked = %d, want 1", report.Ranked)
	}
}

func TestTopByThroughputKeepsThePoolUsableAndSaysSo(t *testing.T) {
	// All candidates unmeasured: returning empty would make the rule's rung
	// unreachable and the symptom would be "the service stopped working" with
	// nothing in the log about a pool. So the filter's members are kept and
	// Filled records that this is what happened.
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop}
	all := byID("a", "b", "c")

	got, report := p.TopByThroughput(all, map[string]float64{}, 2)
	if len(got) != 3 {
		t.Errorf("members = %v, want the filter's own three", nodeIDs(got))
	}
	if !report.Filled {
		t.Error("Filled is false, so an unmeasured pool looks like a ranked one")
	}
	if report.Unmeasured != 3 {
		t.Errorf("unmeasured = %d, want 3", report.Unmeasured)
	}
}

func TestTopByThroughputSkipsTheExerciseWhenThereIsNothingToNarrow(t *testing.T) {
	// Under the limit there is no ranking to do, so claiming one would be a lie
	// the report tells: Ranked counts what was actually ordered.
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop}
	all := byID("a", "b")

	got, report := p.TopByThroughput(all, map[string]float64{"a": 1}, 5)
	if len(got) != 2 {
		t.Errorf("members = %v", nodeIDs(got))
	}
	// A plain pass reports no measurement drama: nothing was unmeasured and
	// nothing had to be filled, and claiming otherwise would make the loud field
	// meaningless.
	if report.Unmeasured != 0 || report.Filled || report.Ranked != 2 || report.Kept != 2 {
		t.Errorf("report = %+v, want a plain un-narrowed pass", report)
	}
}
