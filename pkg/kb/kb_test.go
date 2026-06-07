package kb

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/strategy"
)

func TestColdStartIsSeedOrder(t *testing.T) {
	k := New()
	got := k.TopNZapret("yt", 3)
	want := strategy.BuiltinZapretSeed[:3] // simple_fake_alt2, alt10, v4
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cold start[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSetZapretSeedBoundsOutput(t *testing.T) {
	k := New()
	k.SetZapretSeed([]string{"alt12", "alt11"})
	got := k.TopNZapret("yt", 5)
	want := []string{"alt12", "alt11"}
	if len(got) != len(want) {
		t.Fatalf("TopNZapret = %v, want only the seeded ids %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TopNZapret[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Learned ranking still applies within the seeded set.
	k.RecordOutcome("yt", "alt11", true, 30)
	k.RecordOutcome("yt", "alt12", false, 0)
	if top := k.TopNZapret("yt", 1); len(top) != 1 || top[0] != "alt11" {
		t.Errorf("after learning, top = %v, want [alt11]", top)
	}
}

func TestExcludeSkipsStrategy(t *testing.T) {
	k := New()
	got := k.TopNZapret("yt", 3, strategy.BuiltinZapretSeed[0])
	for _, id := range got {
		if id == strategy.BuiltinZapretSeed[0] {
			t.Fatalf("excluded strategy %q present in %v", strategy.BuiltinZapretSeed[0], got)
		}
	}
	if got[0] != strategy.BuiltinZapretSeed[1] { // alt10 becomes first
		t.Errorf("after exclude, first = %q, want %q", got[0], strategy.BuiltinZapretSeed[1])
	}
}

func TestLearnedRankingBeatsSeed(t *testing.T) {
	k := New()
	top := strategy.BuiltinZapretSeed[0]  // simple_fake_alt2 (seed #1)
	good := strategy.BuiltinZapretSeed[4] // simple_fake (seed #5)

	// Tank the seed leader, boost a lower-seeded strategy.
	for i := 0; i < 40; i++ {
		k.RecordOutcome("yt", top, false, 20)
		k.RecordOutcome("yt", good, true, 20)
	}

	got := k.TopNZapret("yt", 10)
	if got[0] != good {
		t.Fatalf("learned: first = %q, want %q (proven). full: %v", got[0], good, got)
	}
	// The tanked strategy should sink below unseen (prior 0.5) ones.
	posTop, posUnseen := indexOf(got, top), indexOf(got, strategy.BuiltinZapretSeed[2]) // v4 unseen
	if posTop < posUnseen {
		t.Errorf("tanked %q (pos %d) should rank below unseen %q (pos %d)", top, posTop, strategy.BuiltinZapretSeed[2], posUnseen)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := t.TempDir() + "/kb.json"
	k := New()
	for i := 0; i < 5; i++ {
		k.RecordOutcome("youtube", "alt12", true, 40)
		k.RecordOutcome("youtube", "alt11", false, 0)
	}
	before12 := k.Stats("youtube", "alt12")
	before11 := k.Stats("youtube", "alt11")
	if err := k.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh KB loads the saved experience instead of cold-starting at the prior.
	k2 := New()
	if err := k2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := k2.Stats("youtube", "alt12"); got.Success != before12.Success || got.RTTms != before12.RTTms {
		t.Errorf("alt12 not restored: got %+v want %+v", got, before12)
	}
	if got := k2.Stats("youtube", "alt11"); got.Success != before11.Success {
		t.Errorf("alt11 success not restored: got %v want %v", got.Success, before11.Success)
	}
}

func TestLoadMissingFileIsColdStart(t *testing.T) {
	if err := New().Load(t.TempDir() + "/nope.json"); err != nil {
		t.Errorf("missing file should be cold start, got err %v", err)
	}
}

func TestSnapshot(t *testing.T) {
	k := New()
	k.RecordOutcome("yt", "alt10", true, 20)
	snap := k.Snapshot()
	if _, ok := snap["yt|alt10"]; !ok {
		t.Fatalf("snapshot missing yt|alt10: %v", snap)
	}
	// Snapshot is a copy: mutating it must not affect the KB.
	snap["yt|alt10"] = 999
	if k.Snapshot()["yt|alt10"] == 999 {
		t.Error("snapshot is not a copy")
	}
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}

// LOT-41: the D-UCB bonus DOES explore — an untried strategy outranks a
// high-count MEDIOCRE one (rate near prior). Together with
// TestLearnedRankingBeatsSeed (a strongly-proven arm stays on top) this pins the
// exploit-leaning balance.
func TestExplorationBonusPrefersUntriedOverMediocre(t *testing.T) {
	k := New()
	mediocre := strategy.BuiltinZapretSeed[0]
	for i := 0; i < 30; i++ { // many samples, rate stays ~prior (alternating)
		k.RecordOutcome("yt", mediocre, i%2 == 0, 20)
	}
	got := k.TopNZapret("yt", 10)
	if got[0] == mediocre {
		t.Errorf("mediocre high-count %q ranked first; the exploration bonus should float an untried one above it: %v", mediocre, got)
	}
}
