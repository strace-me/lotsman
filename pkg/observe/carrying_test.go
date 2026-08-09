package observe

import (
	"strings"
	"testing"
)

// The oracle answers from a DELTA, so every case here is about what changed
// between two passes rather than what a counter happens to hold.
func TestCarryingJudgesMovementNotExistence(t *testing.T) {
	c := DefaultCarrying()
	m := func(bytes int64, flows int, stalled float64) ServiceMetrics {
		return ServiceMetrics{Bytes: bytes, Flows: flows, StalledRatio: stalled}
	}

	// A cumulative total that has been large since boot says nothing about now.
	if _, ok := c.Reason("discord", m(5<<20, 4, 0)); ok {
		t.Error("the first observation must not count as movement")
	}
	// A flow that exists while its counter stopped is exactly what a frozen path
	// looks like, and in one snapshot it is indistinguishable from a busy one.
	if _, ok := c.Reason("discord", m(5<<20, 4, 0)); ok {
		t.Error("an unchanged byte total is not movement")
	}
	reason, ok := c.Reason("discord", m(5<<20+(256<<10), 4, 0))
	if !ok {
		t.Fatal("256 KiB across live flows must count as carrying")
	}
	if !strings.Contains(reason, "KiB") {
		t.Errorf("the reason must carry the evidence, got %q", reason)
	}
	// 768 KiB across 4 flows is 192 KiB EACH, which is not a freeze by any
	// measurement — the freeze is ten flows thrashing at ~3 KiB. The ratio is
	// reported in the evidence and does not decide.
	if reason, ok := c.Reason("discord", m(6<<20, 4, 0.9)); !ok {
		t.Error("bytes say it is carrying; a ratio must not overrule them")
	} else if !strings.Contains(reason, "frozen") {
		t.Errorf("the ratio must still be reported as evidence, got %q", reason)
	}
	// And the real freeze, which the byte floors catch on their own: ten flows
	// thrashing at ~3 KiB each. This is the case the ratio gate was reaching for,
	// and it never needed the ratio.
	c.Reason("x", m(1<<20, 10, 0.9))
	if _, ok := c.Reason("x", m(1<<20+(29<<10), 10, 0.9)); ok {
		t.Error("29 KiB across ten flows is the freeze signature and must not count")
	}
	// A trickle is a keepalive, not use.
	c.Reason("discord", m(6<<20, 4, 0))
	if _, ok := c.Reason("discord", m(6<<20+1024, 4, 0)); ok {
		t.Error("1 KiB is a keepalive, not traffic")
	}
	if _, ok := c.Reason("nosuch", ServiceMetrics{}); ok {
		t.Error("an unobserved rule cannot be carrying")
	}
}

// The per-flow floor is the load-bearing one, and the defect it exists for is a
// browser thrashing: 1110 KiB reads as plenty until you divide it by 387
// connections and get 2.9 KiB each — which is the TSPU freeze, not health.
// Summing turns the symptom into the evidence (principle 11).
func TestCarryingDividesByFlowsBeforeBelievingTheTotal(t *testing.T) {
	thrash := DefaultCarrying()
	thrash.Reason("web", ServiceMetrics{Bytes: 0, Flows: 387})
	if _, ok := thrash.Reason("web", ServiceMetrics{Bytes: 1110 << 10, Flows: 387}); ok {
		t.Error("1110 KiB across 387 flows is 2.9 KiB each — the freeze, not traffic")
	}

	stream := DefaultCarrying()
	stream.Reason("web", ServiceMetrics{Bytes: 0, Flows: 4})
	if _, ok := stream.Reason("web", ServiceMetrics{Bytes: 1110 << 10, Flows: 4}); !ok {
		t.Error("the same volume across 4 flows is real use and must count")
	}
}
