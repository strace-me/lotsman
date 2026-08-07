package core

import (
	"slices"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// The knowledge base can rank a strategy id this machine has no recipe for — the
// ROUTER's `alt12` script name turned up in the laptop's KB and four rules had the
// brain asking for it while nfqws ran something else. Worse, it is
// self-sustaining: the brain names the id, the prober records outcomes under it,
// the KB's confidence grows, the brain names it again, and nothing in that loop
// ever tries to render it.
func TestRecommendationsAreNarrowedToRenderableRecipes(t *testing.T) {
	c := &Core{}
	c.setRenderableRecipes([]strategycat.Recipe{{ID: "flowseal-alt12-google"}, {ID: "flowseal-syndata"}})

	rec := renderableRecommender{
		kb:    fixedRecommender{"alt12", "flowseal-syndata", "router-only", "flowseal-alt12-google"},
		known: c.knownRecipe,
	}
	got := rec.TopNZapret("x", 16)
	want := []string{"flowseal-syndata", "flowseal-alt12-google"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want only the ids with a recipe behind them (%v)", got, want)
	}

	// No desync executor here at all: an empty set must not strip the advice for a
	// reason that has nothing to do with the advice.
	empty := &Core{}
	rec2 := renderableRecommender{kb: fixedRecommender{"alt12"}, known: empty.knownRecipe}
	if got := rec2.TopNZapret("x", 16); !slices.Equal(got, []string{"alt12"}) {
		t.Errorf("with no recipe set the filter must pass everything, got %v", got)
	}
}

type fixedRecommender []string

func (f fixedRecommender) TopNZapret(string, int, ...string) []string { return f }
