package main

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// The applyable set is what the launcher guard checks before going live: every id
// in it must have a <id>.sh in the script dir, because any of them could be
// symlinked as active.sh. A rung pinned to "auto" therefore demanded
// /opt/zapret-lotsman/auto.sh — a launcher that is never supposed to exist — and
// refused to start the daemon.
//
// That is not a hypothetical: v1 spelled an unpinned ALT_ZAPRET rung as an EMPTY
// strategy_id, and the v1→v2 migration turns exactly that into "auto" (see
// configv2.AutoStrategy). Every config migrated off v1 carried it, so every one
// of them hit the guard and stopped booting. "auto" is resolved to a concrete
// recipe at escalation by core.chosenRecipe, so it never reaches the executor as
// a name and must not be counted here.
func TestZapretStrategyIDsSkipsAutoSentinel(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{
		"youtube": {Chain: []registry.ChainStep{
			{StrategyClass: strategy.ClassZapret, StrategyID: "auto"},
		}},
	}}
	got := zapretStrategyIDs(reg, strategy.BuiltinCatalog())

	for _, id := range got {
		if id == "auto" {
			t.Fatalf("auto sentinel counted as an applyable strategy: %v", got)
		}
	}
	// The builtin catalog is still in scope: alt12/alt11 are real launchers on the
	// router, and dropping them would mean never composing anything.
	if len(got) == 0 {
		t.Fatal("builtin catalog strategies dropped from the applyable set")
	}
}

// A pinned concrete strategy must survive the filter — that is the whole point of
// the guard, which exists so a symlink cannot dangle into an nfqws outage.
func TestZapretStrategyIDsKeepsPinnedStrategy(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{
		"discord": {Chain: []registry.ChainStep{
			{StrategyClass: strategy.ClassZapret, StrategyID: "alt12"},
		}},
	}}
	got := zapretStrategyIDs(reg, strategy.BuiltinCatalog())

	found := false
	for _, id := range got {
		if id == "alt12" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pinned strategy alt12 dropped from the applyable set: %v", got)
	}
}
