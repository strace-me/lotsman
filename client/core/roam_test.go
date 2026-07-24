package core

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/client/platform/netid"
	"github.com/strace-me/lotsman/pkg/kb"
)

func roamCore(t *testing.T) (*Core, string) {
	t.Helper()
	dir := t.TempDir()
	c := &Core{
		kb:           kb.New(),
		log:          slog.New(slog.DiscardHandler),
		opts:         Options{KBDir: dir},
		currentNetID: "netA",
		kbFile:       filepath.Join(dir, "netA.json"),
	}
	return c, dir
}

func TestMaybeRoamDebouncesThenSwapsTheKB(t *testing.T) {
	c, dir := roamCore(t)
	ctx := context.Background()
	c.kb.RecordOutcome("youtube", "recipeA", true, 10) // learned on network A

	// Seed network B's store with different learning.
	kbB := kb.New()
	kbB.RecordOutcome("discord", "recipeB", true, 10)
	if err := kbB.Save(filepath.Join(dir, "netB.json")); err != nil {
		t.Fatal(err)
	}

	// First sighting of B: debounced, no swap.
	if c.maybeRoam(ctx, netid.Network{Id: "netB"}) {
		t.Fatal("the first sighting of a new network must be debounced, not swapped")
	}
	if c.currentNetID != "netA" {
		t.Fatal("must not have swapped after one sighting")
	}

	// Second consecutive sighting: swap.
	if !c.maybeRoam(ctx, netid.Network{Id: "netB"}) {
		t.Fatal("a change persisting roamDebounce polls must swap")
	}
	if c.currentNetID != "netB" || c.kbFile != filepath.Join(dir, "netB.json") {
		t.Errorf("after swap: net=%s kb=%s", c.currentNetID, c.kbFile)
	}
	// Live KB is now B's, and A's is gone from memory.
	if !c.kb.Stats("discord", "recipeB").Seen {
		t.Error("network B's learning must be loaded after the swap")
	}
	if c.kb.Stats("youtube", "recipeA").Seen {
		t.Error("network A's learning must be discarded from the live KB")
	}
	// A's learning was persisted before the swap.
	back := kb.New()
	if err := back.Load(filepath.Join(dir, "netA.json")); err != nil {
		t.Fatal(err)
	}
	if !back.Stats("youtube", "recipeA").Seen {
		t.Error("network A's KB must have been saved to disk before swapping away")
	}
}

func TestMaybeRoamIgnoresSameNetworkAndTransientDisconnect(t *testing.T) {
	c, _ := roamCore(t)
	ctx := context.Background()

	if c.maybeRoam(ctx, netid.Network{Id: "netA"}) {
		t.Error("the current network must never trigger a swap")
	}
	// One sighting of B starts the debounce...
	c.maybeRoam(ctx, netid.Network{Id: "netB"})
	if c.pendingCount != 1 {
		t.Fatalf("pendingCount = %d, want 1", c.pendingCount)
	}
	// ...a transient disconnect (Fallback) must reset it, not swap to the shared id.
	if c.maybeRoam(ctx, netid.Network{Id: netid.Fallback}) {
		t.Error("a transient disconnect must not swap the KB")
	}
	if c.pendingCount != 0 || c.currentNetID != "netA" {
		t.Errorf("Fallback must reset debounce and keep netA; count=%d net=%s", c.pendingCount, c.currentNetID)
	}
}

// A cold move — the new network has never been seen — must leave a clean empty KB
// to learn into, not carry the old network's recipes.
func TestMaybeRoamToAColdNetworkStartsEmpty(t *testing.T) {
	c, _ := roamCore(t)
	ctx := context.Background()
	c.kb.RecordOutcome("youtube", "recipeA", true, 10)

	c.maybeRoam(ctx, netid.Network{Id: "cafe"})
	c.maybeRoam(ctx, netid.Network{Id: "cafe"}) // debounce satisfied -> swap

	if c.currentNetID != "cafe" {
		t.Fatalf("expected swap to cafe, got %s", c.currentNetID)
	}
	if c.kb.Stats("youtube", "recipeA").Seen {
		t.Error("a never-seen network must start from an empty KB, not the previous network's")
	}
}
