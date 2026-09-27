package kb

import (
	"os"
	"path/filepath"
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
	k.RecordOutcome(Nfqws1Scope("yt", "alt11"), true, 30)
	k.RecordOutcome(Nfqws1Scope("yt", "alt12"), false, 0)
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
		k.RecordOutcome(Nfqws1Scope("yt", top), false, 20)
		k.RecordOutcome(Nfqws1Scope("yt", good), true, 20)
	}

	got := k.TopNZapret("yt", 10)
	if got[0] != good {
		t.Fatalf("learned: first = %q, want %q (proven). full: %v", got[0], good, got)
	}
	// The tanked strategy should sink below unseen (prior 0.5) ones — or, since 40
	// consecutive failures also trip the circuit breaker (LOT-41 inc3), be excluded
	// entirely (posTop == -1), which is an even stronger "ranks below unseen".
	posTop, posUnseen := indexOf(got, top), indexOf(got, strategy.BuiltinZapretSeed[2]) // v4 unseen
	if posTop != -1 && posTop < posUnseen {
		t.Errorf("tanked %q (pos %d) should rank below unseen %q (pos %d)", top, posTop, strategy.BuiltinZapretSeed[2], posUnseen)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := t.TempDir() + "/kb.json"
	k := New()
	for i := 0; i < 5; i++ {
		k.RecordOutcome(Nfqws1Scope("youtube", "alt12"), true, 40)
		k.RecordOutcome(Nfqws1Scope("youtube", "alt11"), false, 0)
	}
	before12 := k.Stats(Nfqws1Scope("youtube", "alt12"))
	before11 := k.Stats(Nfqws1Scope("youtube", "alt11"))
	if err := k.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh KB loads the saved experience instead of cold-starting at the prior.
	k2 := New()
	if err := k2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := k2.Stats(Nfqws1Scope("youtube", "alt12")); got.Success != before12.Success || got.RTTms != before12.RTTms {
		t.Errorf("alt12 not restored: got %+v want %+v", got, before12)
	}
	if got := k2.Stats(Nfqws1Scope("youtube", "alt11")); got.Success != before11.Success {
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
	k.RecordOutcome(Nfqws1Scope("yt", "alt10"), true, 20)
	snap := k.Snapshot()
	full := Nfqws1Scope("yt", "alt10").Key()
	if _, ok := snap[full]; !ok {
		t.Fatalf("snapshot missing %s: %v", full, snap)
	}
	// Snapshot is a copy: mutating it must not affect the KB.
	snap[full] = 999
	if k.Snapshot()[full] == 999 {
		t.Error("snapshot is not a copy")
	}
}

// LOT-41 inc3: breakerThreshold consecutive failures quarantine a strategy —
// TopNZapret stops returning it so the chain skips a proven-dead pick.
func TestBreakerQuarantinesPersistentFailer(t *testing.T) {
	k := New()
	bad := strategy.BuiltinZapretSeed[0]
	for i := 0; i < breakerThreshold; i++ {
		k.RecordOutcome(Nfqws1Scope("yt", bad), false, 0)
	}
	if got := k.TopNZapret("yt", 10); indexOf(got, bad) != -1 {
		t.Fatalf("persistent failer %q should be quarantined out of %v", bad, got)
	}
}

// LOT-41 inc3: a quarantine lifts after breakerCooldownTicks Decay ticks (half-open).
func TestBreakerReleasesAfterCooldown(t *testing.T) {
	k := New()
	bad := strategy.BuiltinZapretSeed[0]
	for i := 0; i < breakerThreshold; i++ {
		k.RecordOutcome(Nfqws1Scope("yt", bad), false, 0)
	}
	if indexOf(k.TopNZapret("yt", 10), bad) != -1 {
		t.Fatal("should be quarantined right after threshold failures")
	}
	for i := 0; i < breakerCooldownTicks; i++ {
		k.Decay(0.95)
	}
	if indexOf(k.TopNZapret("yt", 10), bad) == -1 {
		t.Fatalf("should be released (half-open) after %d cooldown ticks", breakerCooldownTicks)
	}
}

// LOT-41 inc3: a single success resets the consecutive-failure streak.
func TestBreakerSuccessResetsStreak(t *testing.T) {
	k := New()
	bad := strategy.BuiltinZapretSeed[0]
	for i := 0; i < breakerThreshold-1; i++ {
		k.RecordOutcome(Nfqws1Scope("yt", bad), false, 0)
	}
	k.RecordOutcome(Nfqws1Scope("yt", bad), true, 20) // resets the streak
	for i := 0; i < breakerThreshold-1; i++ {
		k.RecordOutcome(Nfqws1Scope("yt", bad), false, 0)
	}
	if indexOf(k.TopNZapret("yt", 10), bad) == -1 {
		t.Fatal("success should reset the failure streak; not enough fresh failures to quarantine")
	}
}

// LOT-41 inc3: the breaker must never strand the chain — if every strategy is
// quarantined, TopNZapret falls back to returning them ranked.
func TestBreakerNeverStrandsChain(t *testing.T) {
	k := New()
	k.SetZapretSeed([]string{"alt12"}) // sole strategy
	for i := 0; i < breakerThreshold; i++ {
		k.RecordOutcome(Nfqws1Scope("yt", "alt12"), false, 0)
	}
	if got := k.TopNZapret("yt", 5); len(got) != 1 || got[0] != "alt12" {
		t.Fatalf("breaker stranded the chain when all strategies quarantined: got %v", got)
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
		k.RecordOutcome(Nfqws1Scope("yt", mediocre), i%2 == 0, 20)
	}
	got := k.TopNZapret("yt", 10)
	if got[0] == mediocre {
		t.Errorf("mediocre high-count %q ranked first; the exploration bonus should float an untried one above it: %v", mediocre, got)
	}
}

// LOT-41: Decay is the freshness mechanism — shrinks observation counts (so an
// un-retried strategy regains an exploration bonus and gets re-validated) WITHOUT
// touching the learned EWMA rate.
func TestDecayFreshness(t *testing.T) {
	k := New()
	id := strategy.BuiltinZapretSeed[0]
	for i := 0; i < 20; i++ {
		k.RecordOutcome(Nfqws1Scope("yt", id), true, 20)
	}
	rateBefore := k.Stats(Nfqws1Scope("yt", id)).Success
	for i := 0; i < 30; i++ {
		k.Decay(0.5)
	}
	if got := k.Stats(Nfqws1Scope("yt", id)).Success; got != rateBefore {
		t.Errorf("Decay must NOT change the EWMA rate: before=%v after=%v", rateBefore, got)
	}
	k.Decay(0)   // out-of-range = no-op
	k.Decay(1.5) // out-of-range = no-op

	// Re-validation via count asymmetry: A and B share equal mediocre history, then
	// only A keeps being re-sampled while B goes stale (decayed). At equal rate the
	// stale B carries the larger exploration bonus, so B ranks ABOVE the freshly-
	// re-sampled A — re-check the one not confirmed lately.
	k2 := New()
	a, b := strategy.BuiltinZapretSeed[0], strategy.BuiltinZapretSeed[1]
	for i := 0; i < 20; i++ {
		k2.RecordOutcome(Nfqws1Scope("yt", a), i%2 == 0, 20)
		k2.RecordOutcome(Nfqws1Scope("yt", b), i%2 == 0, 20)
	}
	for i := 0; i < 8; i++ {
		k2.Decay(0.5)
	}
	for i := 0; i < 20; i++ {
		k2.RecordOutcome(Nfqws1Scope("yt", a), i%2 == 0, 20) // re-sample A only; B stays stale
	}
	got := k2.TopNZapret("yt", 20)
	if indexOf(got, b) >= indexOf(got, a) {
		t.Errorf("stale B should rank above freshly-resampled A (freshness re-validation): %v", got)
	}
}

func TestNetworkPriorAggregatesAndExcludesSelf(t *testing.T) {
	k := New()
	for i := 0; i < 6; i++ {
		k.RecordOutcome(Nfqws1Scope("youtube", "R"), true, 10) // youtube: R works
	}
	for i := 0; i < 6; i++ {
		k.RecordOutcome(Nfqws1Scope("tiktok", "R"), false, 0) // tiktok: R fails
	}

	// discord never tried R: prior blends youtube (high) and tiktok (low).
	succ, n := k.NetworkPrior(EngineNfqws1, LegacyDefaultPool, "R", "discord")
	if n == 0 {
		t.Fatal("prior should aggregate youtube + tiktok")
	}
	if succ <= 0 || succ >= 1 {
		t.Errorf("blended prior = %v, want strictly between the two extremes", succ)
	}

	// Querying for youtube excludes youtube's own success, leaving only tiktok's
	// failure — the aggregate must drop.
	succNoYT, _ := k.NetworkPrior(EngineNfqws1, LegacyDefaultPool, "R", "youtube")
	if !(succNoYT < succ) {
		t.Errorf("excluding youtube should lower the aggregate: %v (excl youtube) vs %v (excl discord)", succNoYT, succ)
	}

	// A recipe nobody tried has no prior.
	if _, n := k.NetworkPrior(EngineNfqws1, LegacyDefaultPool, "never", "discord"); n != 0 {
		t.Errorf("an untried recipe must have no prior, got n=%v", n)
	}
}

func TestReloadReplacesRatherThanMerges(t *testing.T) {
	dir := t.TempDir()
	// KB for network A, saved to disk.
	a := New()
	a.RecordOutcome(Nfqws1Scope("youtube", "recipeA"), true, 10)
	if err := a.Save(dir + "/a.json"); err != nil {
		t.Fatal(err)
	}
	// KB for network B, saved to disk.
	b := New()
	b.RecordOutcome(Nfqws1Scope("discord", "recipeB"), true, 10)
	if err := b.Save(dir + "/b.json"); err != nil {
		t.Fatal(err)
	}

	// Live KB currently holds A; Reload to B must DISCARD A, not merge.
	live := New()
	if err := live.Load(dir + "/a.json"); err != nil {
		t.Fatal(err)
	}
	if err := live.Reload(dir + "/b.json"); err != nil {
		t.Fatal(err)
	}
	if !live.Stats(Nfqws1Scope("discord", "recipeB")).Seen {
		t.Error("Reload must load the new network's records")
	}
	if live.Stats(Nfqws1Scope("youtube", "recipeA")).Seen {
		t.Error("Reload must DISCARD the previous network's records, not merge them")
	}

	// Reloading a not-seen-before network (missing file) is a clean cold start.
	if err := live.Reload(dir + "/never.json"); err != nil {
		t.Fatalf("reload of a missing file must be a cold start, got %v", err)
	}
	if live.Stats(Nfqws1Scope("discord", "recipeB")).Seen {
		t.Error("a cold reload must leave an empty KB")
	}
}

// The reason the engine is in the key at all: nfqws1 and nfqws2 both ship a
// strategy called alt12, and a verdict from one says nothing about the other.
// Keyed on (service, strategy) alone the two blend and the ranker degrades
// silently — nothing reports it, the numbers just stop meaning anything.
func TestTwoEnginesSharingAStrategyIdDoNotBlend(t *testing.T) {
	k := New()
	one := Scope{Service: "yt", Engine: EngineNfqws1, Pool: LegacyDefaultPool, StrategyID: "alt12"}
	two := Scope{Service: "yt", Engine: EngineNfqws2, Pool: LegacyDefaultPool, StrategyID: "alt12"}

	for i := 0; i < 5; i++ {
		k.RecordOutcome(one, true, 10)
	}
	for i := 0; i < 5; i++ {
		k.RecordOutcome(two, false, 0)
	}

	if got := k.Stats(one).Success; got < 0.5 {
		t.Errorf("nfqws1 alt12 lost its own record: %v", got)
	}
	if got := k.Stats(two).Success; got > 0.5 {
		t.Errorf("nfqws2 alt12 inherited nfqws1's successes: %v", got)
	}
	if one.Key() == two.Key() {
		t.Fatal("the two scopes produced the same key")
	}
}

// A pre-namespace kb.json must keep its measured experience: those records were
// all learned through the single nfqws1 instance with no pool named, so they read
// back as nfqws1|legacy-default rather than being discarded.
func TestLoadMigratesLegacyKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kb.json")
	legacy := `{"records":{"yt|alt10":{"ewma":0.8,"rtt":40,"jitter":3,"count":12},
	                "yt|alt12":{"ewma":0.3,"rtt":90,"jitter":40,"count":9}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	k := New()
	if err := k.Load(path); err != nil {
		t.Fatal(err)
	}
	stats := k.Stats(Nfqws1Scope("yt", "alt10"))
	if !stats.Seen {
		t.Fatal("a legacy record was dropped instead of migrated")
	}
	if stats.Success < 0.79 || stats.Success > 0.81 {
		t.Errorf("migrated value = %v, want ~0.8", stats.Success)
	}
	if k.Stats(Scope{Service: "yt", Engine: EngineNfqws2, Pool: LegacyDefaultPool, StrategyID: "alt10"}).Seen {
		t.Error("a legacy record became visible to an engine that never ran it")
	}
	// An already-migrated file must round-trip unchanged.
	if err := k.Save(path); err != nil {
		t.Fatal(err)
	}
	again := New()
	if err := again.Load(path); err != nil {
		t.Fatal(err)
	}
	if again.Stats(Nfqws1Scope("yt", "alt10")).Success != stats.Success {
		t.Error("a migrated key was re-migrated on the next load")
	}
}
