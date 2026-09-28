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
	if report.Kept != 2 || report.Ranked != 3 || report.Unknown != 0 || report.Frozen != 0 {
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
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop, Limit: 2}
	// Enough measured candidates that the coverage gate is satisfied — this test
	// is about what an unmeasured node does to the RANKING, not about the gate,
	// which has its own case above. The limit still sits below the admitted count
	// or this takes the early "nothing to narrow" path and never sorts.
	all := byID("measured", "fastest", "middling", "unknown", "zeroed")
	goodput := map[string]float64{"measured": 800, "fastest": 900, "middling": 600, "zeroed": 0}

	got, report := p.TopByThroughput(all, goodput, 2)
	// The zero measurement is a FROZEN exit and the absent one is merely idle;
	// neither belongs in a throughput race, and neither outranks a real rate.
	if want := []string{"fastest", "measured"}; !equal(nodeIDs(got), want) {
		t.Errorf("members = %v, want %v", nodeIDs(got), want)
	}
	if report.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", report.Unknown)
	}
	// A frozen node was measured; only the absent one is unknown. Sharing one
	// counter for them would understate coverage and make the gate refuse a
	// decision the data supports.
	if report.Frozen != 1 {
		t.Errorf("frozen = %d, want 1", report.Frozen)
	}
	if report.Measured != 4 {
		t.Errorf("measured = %d, want 4", report.Measured)
	}
	if report.Ranked != 3 {
		t.Errorf("ranked = %d, want 3", report.Ranked)
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
	if report.Unknown != 3 {
		t.Errorf("unknown = %d, want 3", report.Unknown)
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
	if report.Unknown != 0 || report.Frozen != 0 || report.Filled || report.Ranked != 2 || report.Kept != 2 {
		t.Errorf("report = %+v, want a plain un-narrowed pass", report)
	}
}

func TestMembershipsRanksOnlyThroughputPoolsAndOnlyWithMeasurements(t *testing.T) {
	// Memberships has six call sites across both roots, so the measurements hang
	// off the Set instead of becoming an argument every front-end must remember
	// to pass. That makes the "no measurements" state the normal one for the
	// one-shot CLI, and it must degrade to the filter rather than narrow on
	// nothing.
	all := byID("a", "b", "c", "d")
	goodput := map[string]float64{"a": 900, "b": 500, "c": 100, "d": 700}

	set := &Set{Pools: map[string]Pool{
		"bulk":    {Name: "bulk", Type: TypeSpeedtestTop, Limit: 2},
		"latency": {Name: "latency", Type: TypeURLTest},
	}, Goodput: goodput}
	out := set.Memberships(all)
	if want := []string{"a", "d"}; !equal(nodeIDs(out["bulk"]), want) {
		t.Errorf("bulk = %v, want %v", nodeIDs(out["bulk"]), want)
	}
	// A url_test pool is a selector, not a ranking: it must be untouched, or a
	// throughput measurement would quietly shrink the pool every failover path
	// depends on.
	if len(out["latency"]) != 4 {
		t.Errorf("latency pool was narrowed: %v", nodeIDs(out["latency"]))
	}
	if set.Reports["bulk"].Unknown != 0 || set.Reports["bulk"].Frozen != 0 || set.Reports["bulk"].Filled {
		t.Errorf("bulk report = %+v", set.Reports["bulk"])
	}

	// No measurements at all: the filter's members stand, and the report says the
	// ranking never ran instead of leaving a normal-looking config behind.
	bare := &Set{Pools: map[string]Pool{"bulk": {Name: "bulk", Type: TypeSpeedtestTop, Limit: 2}}}
	out = bare.Memberships(all)
	if len(out["bulk"]) != 4 {
		t.Errorf("unmeasured bulk pool = %v, want the filter's own four", nodeIDs(out["bulk"]))
	}
	report := bare.Reports["bulk"]
	if !report.Filled || report.Ranked != 0 || report.Unknown != 4 {
		t.Errorf("report = %+v, want an unmeasured pool reported as such", report)
	}
}

// The regression this gate exists to prevent, and it is the one that matters:
// the measurement is inbound, so in LOT-69's own state — ten rules on one exit —
// exactly one candidate is measured. Ranking on that subset alone keeps that one
// exit, collapses the pool to a single member, and looks like a successful
// narrowing while locking in the monoculture.
func TestTopByThroughputRefusesToRankOnThinCoverage(t *testing.T) {
	// 20 candidates, one of them ever used. Coverage is 5%.
	var all []subscription.Node
	for _, id := range []string{"used", "u2", "u3", "u4", "u5", "u6", "u7", "u8", "u9", "u10",
		"u11", "u12", "u13", "u14", "u15", "u16", "u17", "u18", "u19", "u20"} {
		all = append(all, subscription.Node{ID: id})
	}
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop, Limit: 3}

	got, report := p.TopByThroughput(all, map[string]float64{"used": 900}, 3)
	if len(got) != 20 {
		t.Fatalf("pool collapsed to %d members: %v", len(got), nodeIDs(got))
	}
	if !report.Uncovered {
		t.Error("a pool covered by one candidate in twenty was ranked without saying so")
	}
	if report.Ranked != 0 {
		t.Errorf("ranked = %d, want 0: the ranking must not have run", report.Ranked)
	}
	if report.Coverage < 0.05 || report.Coverage > 0.06 {
		t.Errorf("coverage = %v, want ~0.05", report.Coverage)
	}
	// Unknown is still counted even though the ranking did not run: the operator
	// needs to know how blind it is, not just that it was declined.
	if report.Unknown != 19 {
		t.Errorf("unknown = %d, want 19", report.Unknown)
	}
}

func TestTopByThroughputRanksOnceCoverageIsReal(t *testing.T) {
	// The gate must not be so high that it disables the feature it guards. A pool
	// measured across most of its fleet ranks normally.
	var all []subscription.Node
	goodput := map[string]float64{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		all = append(all, subscription.Node{ID: id})
		goodput[id] = 100
	}
	goodput["a"] = 900
	goodput["b"] = 800
	p := Pool{Name: "bulk", Type: TypeSpeedtestTop, Limit: 2}

	got, report := p.TopByThroughput(all, goodput, 2)
	if want := []string{"a", "b"}; !equal(nodeIDs(got), want) {
		t.Errorf("members = %v, want %v", nodeIDs(got), want)
	}
	if report.Uncovered {
		t.Error("a fully covered pool was declined as uncovered")
	}
}

func TestCoverageThresholdIsConfigurable(t *testing.T) {
	// An operator with a small, hand-managed fleet should be able to require
	// strictly complete coverage, and one who trusts partial observations should
	// be able to relax it. Neither is a judgement the code should make for them.
	var all []subscription.Node
	for _, id := range []string{"a", "b", "c", "d"} {
		all = append(all, subscription.Node{ID: id})
	}
	measured := map[string]float64{"a": 10, "b": 20}

	strict := Pool{Name: "bulk", Type: TypeSpeedtestTop, Limit: 1, MinCoverage: 1.0}
	if _, report := strict.TopByThroughput(all, measured, 1); !report.Uncovered {
		t.Error("a MinCoverage of one accepted only half the pool")
	}
	relaxed := Pool{Name: "bulk", Type: TypeSpeedtestTop, Limit: 1, MinCoverage: 0.25}
	if _, report := relaxed.TopByThroughput(all, measured, 1); report.Uncovered {
		t.Error("a low MinCoverage still rejected half the pool")
	}
}

// Regression: "no measurement" and "measured, carried nothing" were counted by one
// field, which understates coverage. A fleet of 20 where 15 carry and 5 are frozen
// is FULLY measured, and reporting it as 75% covered makes the gate refuse a
// decision the data supports — the pool stays wide for no stated reason.
func TestCoverageCountsFrozenExitsAsMeasured(t *testing.T) {
	var all []subscription.Node
	goodput := map[string]float64{}
	// 15 exits carry, 5 are frozen: measured at zero, not absent.
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10",
		"c11", "c12", "c13", "c14", "c15"} {
		all = append(all, subscription.Node{ID: id})
		goodput[id] = 400
	}
	for _, id := range []string{"f1", "f2", "f3", "f4", "f5"} {
		all = append(all, subscription.Node{ID: id})
		goodput[id] = 0
	}

	p := Pool{Name: "bulk", Type: TypeSpeedtestTop, Limit: 3}
	_, report := p.TopByThroughput(all, goodput, 3)

	if report.Unknown != 0 {
		t.Errorf("unknown = %d, want 0: every node carries a reading", report.Unknown)
	}
	if report.Frozen != 5 {
		t.Errorf("frozen = %d, want 5", report.Frozen)
	}
	if report.Measured != 20 {
		t.Errorf("measured = %d, want 20: a frozen exit was measured", report.Measured)
	}
	if report.Coverage != 1 {
		t.Errorf("coverage = %v, want 1: nothing here is unknown", report.Coverage)
	}
	if report.Uncovered {
		t.Error("a fully measured fleet was refused as uncovered")
	}
	// And the frozen ones still must not win a throughput race.
	if report.Ranked != 15 {
		t.Errorf("ranked = %d, want 15: frozen exits are not candidates", report.Ranked)
	}
}
