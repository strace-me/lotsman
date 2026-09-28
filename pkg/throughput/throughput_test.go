package throughput

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type nopLog struct{}

func (nopLog) Info(string, ...any) {}
func (nopLog) Warn(string, ...any) {}

func at(base time.Time, d time.Duration) func() time.Time {
	return func() time.Time { return base.Add(d) }
}

func newScanner(t *testing.T, base time.Time) (*Scanner, []string) {
	t.Helper()
	var nodes []string
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		nodes = append(nodes, id)
	}
	return &Scanner{Store: OpenStore(""), Log: nopLog{}}, nodes
}

// The whole reason this package exists: a budget spent on nodes that already have
// a number never raises coverage, and coverage is what the pool's gate waits on.
func TestPassSpendsItsBudgetOnUnmeasuredNodes(t *testing.T) {
	fleet := []string{"a", "b", "c", "d", "e", "f"}
	var seen []string
	s := &Scanner{Store: OpenStore(""), Log: nopLog{},
		Measure: func(_ context.Context, node string) (float64, bool) {
			seen = append(seen, node)
			return 100, true
		}}

	// A budget of two per pass, three passes: coverage must reach 100% and the
	// same node must never be measured twice while others wait unmeasured.
	for i := 0; i < 3; i++ {
		report := s.Pass(context.Background(), fleet, 2)
		if report.Attempted != 2 {
			t.Fatalf("pass %d attempted %d, want 2", i, report.Attempted)
		}
	}
	measured, fraction := s.Store.Coverage(fleet)
	if measured != 6 || fraction != 1 {
		t.Errorf("coverage = %d/%d (%v), want 6/6", measured, len(fleet), fraction)
	}
	counts := map[string]int{}
	for _, n := range seen {
		counts[n]++
	}
	for node, n := range counts {
		if n != 1 {
			t.Errorf("node %q measured %d times while others were unmeasured", node, n)
		}
	}
}

func TestPassRecordsFailureAsAMeasurement(t *testing.T) {
	// A node that could not carry is a measurement. Dropping it would make the
	// next pass treat it as unmeasured, and unmeasured nodes are exactly the ones
	// the coverage gate refuses to rank on.
	fleet := []string{"a", "b"}
	s := &Scanner{Store: OpenStore(""), Log: nopLog{},
		Measure: func(_ context.Context, node string) (float64, bool) {
			return 0, false
		}}

	report := s.Pass(context.Background(), fleet, 5)
	if report.Failed != 2 || report.Measured != 0 {
		t.Errorf("report = %+v", report)
	}
	measured, _ := s.Store.Coverage(fleet)
	if measured != 2 {
		t.Errorf("failures did not count as measured: %d", measured)
	}
	// A frozen exit records zero, and zero is a rate here — it must not be
	// confused with the absence of a measurement.
	fresh := s.Store.Fresh()
	if v, ok := fresh["a"]; !ok || v != 0 {
		t.Errorf("frozen node = %v (present=%v), want a recorded zero", v, ok)
	}
}

func TestReadingsGoStale(t *testing.T) {
	// A reading from yesterday does not describe today's exit. Letting it win a
	// ranking would present a claim about the past as a fact about now — and
	// because coverage shrinks with staleness, the pool gate closes on its own
	// rather than trusting an old number indefinitely.
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	fleet := []string{"a", "b"}
	store := OpenStore("")
	store.now = at(base, 0)
	store.Record("a", 900)
	store.Record("b", 700)

	if _, fraction := store.Coverage(fleet); fraction != 1 {
		t.Fatalf("fresh coverage = %v, want 1", fraction)
	}
	store.now = at(base, 31*time.Minute)
	measured, fraction := store.Coverage(fleet)
	if measured != 0 || fraction != 0 {
		t.Errorf("stale coverage = %d (%v), want 0", measured, fraction)
	}
	if len(store.Fresh()) != 0 {
		t.Errorf("stale readings were still fresh: %v", store.Fresh())
	}
}

func TestCoverageSurvivesARestart(t *testing.T) {
	// Without persistence every restart returns the gate to "too few measured",
	// so the pool widens each time the box reboots and the ranking never takes
	// hold no matter how many passes have run.
	dir := t.TempDir()
	path := filepath.Join(dir, "throughput.json")
	fleet := []string{"a", "b", "c"}

	first := OpenStore(path)
	first.now = func() time.Time { return time.Now() }
	first.Record("a", 900)
	first.Record("b", 800)
	first.Record("c", 700)

	second := OpenStore(path)
	if _, fraction := second.Coverage(fleet); fraction != 1 {
		t.Errorf("coverage after reload = %v, want 1", fraction)
	}
}

func TestACorruptStoreStartsOverRatherThanRefusingToStart(t *testing.T) {
	// Refusing to boot over a scratch measurement would be a worse outcome than
	// re-measuring: the box has no VPN, and the thing that would fix it is the
	// scan that just refused to run.
	path := filepath.Join(t.TempDir(), "throughput.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := OpenStore(path)
	if measured, _ := store.Coverage([]string{"a"}); measured != 0 {
		t.Errorf("a corrupt file produced %d measurements", measured)
	}
	store.Record("a", 10)
	if measured, _ := store.Coverage([]string{"a"}); measured != 1 {
		t.Error("the store did not recover after a corrupt file")
	}
}

func TestAFullyCoveredFleetRefreshesTheStalestFirst(t *testing.T) {
	// Coverage that never refreshes is coverage that quietly goes stale and takes
	// the gate down with it, so a covered fleet still gets maintenance passes —
	// oldest reading first.
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	fleet := []string{"a", "b", "c"}
	store := OpenStore("")
	store.now = at(base, 0)
	store.Record("b", 500)
	store.now = at(base, 10*time.Minute)
	store.Record("a", 500)
	store.now = at(base, 20*time.Minute)
	store.Record("c", 500)

	s := &Scanner{Store: store, Log: nopLog{},
		Measure: func(_ context.Context, node string) (float64, bool) { return 900, true }}
	s.Pass(context.Background(), fleet, 1)

	if got := store.Fresh()["b"]; got != 900 {
		t.Errorf("b = %v, want it refreshed first (it was the stalest)", got)
	}
}

func TestPassStopsWhenTheContextIsDone(t *testing.T) {
	// A scan that keeps pulling bytes after shutdown is the defect LOT-67 hit:
	// it spent real uplink on the very link it was measuring, and had to refuse
	// mid-session.
	fleet := []string{"a", "b", "c", "d"}
	calls := 0
	s := &Scanner{Store: OpenStore(""), Log: nopLog{},
		Measure: func(_ context.Context, node string) (float64, bool) {
			calls++
			return 100, true
		}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := s.Pass(ctx, fleet, 4)
	if report.Attempted != 0 || calls != 0 {
		t.Errorf("a cancelled pass still measured: attempted=%d calls=%d", report.Attempted, calls)
	}
}
