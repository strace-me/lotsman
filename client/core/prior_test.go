package core

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/kb"
)

func TestBlendRecipePrior(t *testing.T) {
	// No prior at all -> own score is returned unchanged (fresh network behaves
	// exactly as before).
	if got := blendRecipePrior(0.5, 0, 0, 0); got != 0.5 {
		t.Errorf("no prior: got %v, want the own score 0.5", got)
	}
	// A cold service (no own observations) leans entirely on the network prior.
	if got := blendRecipePrior(0.5, 0, 0.9, 10); got != 0.9 {
		t.Errorf("cold service: got %v, want the prior 0.9", got)
	}
	// A well-sampled own signal dominates the prior.
	if got := blendRecipePrior(0.2, 100, 0.9, 10); got > 0.3 {
		t.Errorf("well-sampled own signal should dominate: got %v, want <0.3", got)
	}
}

// The point of the whole feature: a service meeting the desync rung for the first
// time on a network should prefer the recipe that already beat that network's DPI
// for another service, over an untried one — instead of blind catalog order.
func TestRecipeScorePrefersNetworkWinnerForColdService(t *testing.T) {
	k := kb.New()
	for i := 0; i < 5; i++ {
		k.RecordOutcome("youtube", "winner", true, 10)
	}
	c := &Core{kb: k}

	win := c.recipeScore("discord", "winner")  // discord never tried it, but youtube won with it here
	unk := c.recipeScore("discord", "untried") // nobody has tried it on this network
	if !(win > unk) {
		t.Errorf("cold discord should rank the network winner (%.3f) above an untried recipe (%.3f)", win, unk)
	}
}

// A recipe that FAILED for others on the network must rank BELOW an untried one,
// so a cold service does not repeat a known local loser.
func TestRecipeScoreAvoidsNetworkLoserForColdService(t *testing.T) {
	k := kb.New()
	for i := 0; i < 5; i++ {
		k.RecordOutcome("youtube", "loser", false, 0)
	}
	c := &Core{kb: k}

	lose := c.recipeScore("discord", "loser")
	unk := c.recipeScore("discord", "untried")
	if !(lose < unk) {
		t.Errorf("cold discord should rank a known network loser (%.3f) below an untried recipe (%.3f)", lose, unk)
	}
}
