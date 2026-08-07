package core

import (
	"context"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// rotCore builds just enough Core for the rotation policy: a rule on the rung, a
// set of renderable recipes, and a KB to score them.
func rotCore(t *testing.T, applied *[]string) *Core {
	t.Helper()
	c := &Core{
		log: slog.New(slog.DiscardHandler),
		reg: &registry.Registry{Services: map[string]registry.Service{
			"youtube": {Name: "youtube"},
		}},
		kb: kb.New(),
	}
	c.zapExec = &zapretExec{
		log:    slog.New(slog.DiscardHandler),
		active: func() []registry.Service { return []registry.Service{{Name: "youtube"}} },
		recipes: []strategycat.Recipe{
			{ID: "loser"}, {ID: "alpha"}, {ID: "bravo"}, {ID: "charlie"}, {ID: "delta"},
		},
		clash: nil,
	}
	// Stand in for Enable, which would drive nft and a real engine.
	c.applyForTest = func(_ context.Context, _, recipe string) error {
		*applied = append(*applied, recipe)
		return nil
	}
	return c
}

// The invariant the owner asked for in one sentence: the canary tests, and only a
// SUCCESS moves the rule's live traffic. A candidate that fails in the sandbox
// must never reach production — that is the difference between finding out a
// recipe is wrong here and finding out because YouTube stopped loading.
func TestRotationMovesProductionOnlyOntoAPassingCandidate(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)

	var tried []string
	c.testCandidate = func(_ context.Context, _ registry.Service, id string) (bool, bool, string) {
		tried = append(tried, id)
		return id == "charlie", true, "measured"
	}

	c.rotateRecipe(context.Background(), "youtube", "loser")

	if len(applied) != 1 || applied[0] != "charlie" {
		t.Fatalf("production moved to %v, want exactly [charlie] — the only candidate that passed", applied)
	}
	for _, id := range tried {
		if id == "loser" {
			t.Error("the recipe that just failed must not be offered back as a candidate")
		}
	}
	// Every verdict was a real measurement of that recipe on this network, so all
	// of them belong in the knowledge base — not just the winner.
	for _, id := range tried {
		if !c.kb.Stats("youtube", id).Seen {
			t.Errorf("sandbox verdict for %q was not recorded", id)
		}
	}
	if s := c.kb.Stats("youtube", "charlie"); s.Success <= 0.5 {
		t.Errorf("the passing candidate scored %v, want above the prior", s.Success)
	}
}

func TestRotationLeavesProductionAloneWhenNothingPasses(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	c.testCandidate = func(context.Context, registry.Service, string) (bool, bool, string) {
		return false, true, "carried nothing"
	}

	c.rotateRecipe(context.Background(), "youtube", "loser")

	if len(applied) != 0 {
		t.Fatalf("production was moved to %v though no candidate passed", applied)
	}
	// It tries a BOUNDED number: rotation is meant to step off a broken recipe,
	// not to search the catalogue, and an unbounded pass would start an engine per
	// recipe while the rule is already down.
	tried := 0
	for _, r := range c.zapExec.recipes {
		if c.kb.Stats("youtube", r.ID).Seen {
			tried++
		}
	}
	if tried > sandboxCandidates {
		t.Errorf("tried %d candidates in one pass, want at most %d", tried, sandboxCandidates)
	}
}

// A rule that keeps failing must not spend the machine on engine starts: each
// test costs a process and an nft table.
func TestRotationHonoursItsCooldown(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	calls := 0
	c.testCandidate = func(context.Context, registry.Service, string) (bool, bool, string) {
		calls++
		return false, true, "no"
	}

	c.rotateRecipe(context.Background(), "youtube", "loser")
	first := calls
	c.rotateRecipe(context.Background(), "youtube", "loser")
	if calls != first {
		t.Errorf("a second pass ran %d more tests inside the cooldown", calls-first)
	}
}

// It must also stop the moment the brain moves the rule elsewhere: a candidate
// applied to a rung the rule has left is a change nobody asked for, and the
// verdict would be filed against a path the rule no longer takes.
func TestRotationStopsWhenTheRuleLeavesTheRung(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	onRung := true
	c.zapExec.active = func() []registry.Service {
		if onRung {
			return []registry.Service{{Name: "youtube"}}
		}
		return nil
	}
	c.testCandidate = func(context.Context, registry.Service, string) (bool, bool, string) {
		onRung = false // the brain escalated while we were measuring
		return true, true, "would have passed"
	}

	c.rotateRecipe(context.Background(), "youtube", "loser")

	if len(applied) != 0 {
		t.Fatalf("applied %v to a rung the rule had already left", applied)
	}
}

// The gate in front of a FIRST application must fail OPEN. A sandbox that is
// merely broken — not built, wrong platform, nothing to fetch — would otherwise
// pin every rule to the tunnel forever and call it caution, which is a far worse
// failure than the one the gate prevents. Build-ahead code that has never met a
// kernel makes this the likely path, not the exotic one.
func TestGateAppliesUnverifiedWhenTheLaneCannotMeasure(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	c.testCandidate = func(context.Context, registry.Service, string) (bool, bool, string) {
		return false, false, "no sandbox on this host"
	}

	c.gateEnable(context.Background(), "youtube")

	if len(applied) != 1 {
		t.Fatalf("applied %v, want exactly one recipe: an unmeasurable lane must not leave the rule unrouted", applied)
	}
	// ...and it must NOT teach the knowledge base. "Could not ask" is not a verdict
	// about the recipe, and recording it would demote a strategy for the sandbox's
	// own breakage.
	for _, r := range c.zapExec.recipes {
		if c.kb.Stats("youtube", r.ID).Seen {
			t.Errorf("recorded an outcome for %q though nothing was measured", r.ID)
		}
	}
}

// And when the lane DOES work, the first application is gated like any other: an
// unproven recipe never reaches the rule's traffic.
func TestGateAppliesOnlyAProvenRecipe(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	c.testCandidate = func(_ context.Context, _ registry.Service, id string) (bool, bool, string) {
		return id == "bravo", true, "measured"
	}

	c.gateEnable(context.Background(), "youtube")

	if len(applied) != 1 || applied[0] != "bravo" {
		t.Fatalf("applied %v, want [bravo] — the only candidate that passed", applied)
	}
}
