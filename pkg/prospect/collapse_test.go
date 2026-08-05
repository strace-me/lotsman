package prospect

import (
	"testing"
	"time"
)

func confirmed(t *testing.T, s *Store, id string) {
	t.Helper()
	s.Won(id, []string{"--dpi-desync=fake"}, "youtube", map[string]string{"method": "fake"})
	s.Won(id, []string{"--dpi-desync=fake"}, "discord", nil)
}

// Independent strategies do not die together by chance. When several do, the
// thing that changed is upstream, and every recorded failure describes a network
// that no longer exists.
func TestCollapseNeedsSeveralAtOnce(t *testing.T) {
	now := time.Unix(0, 0)
	s, _ := Open("", func() time.Time { return now })
	for _, id := range []string{"a", "b", "c"} {
		confirmed(t, s, id)
	}

	// One strategy dying is ordinary attrition, not a signal.
	for i := 0; i < demoteAt; i++ {
		s.Lost("a")
	}
	if s.Collapsed(now, 24*time.Hour, 3) {
		t.Error("a single demotion was read as a collapse")
	}

	for i := 0; i < demoteAt; i++ {
		s.Lost("b")
		s.Lost("c")
	}
	if !s.Collapsed(now, 24*time.Hour, 3) {
		t.Error("three demotions in one window were not read as a collapse")
	}
	// Spread far enough apart, the same three are attrition rather than an event.
	if s.Collapsed(now.Add(48*time.Hour), time.Hour, 3) {
		t.Error("demotions outside the window still counted")
	}
}

// The failures were recorded against a network that has since changed, so
// keeping them would rule out strategies for reasons that no longer apply. Wins
// are kept: a strategy that once carried traffic is still a better lead than one
// that never did.
func TestRearmForgetsFailuresAndKeepsWins(t *testing.T) {
	now := time.Unix(0, 0)
	s, _ := Open("", func() time.Time { return now })
	confirmed(t, s, "a")
	for i := 0; i < demoteAt; i++ {
		s.Lost("a")
	}
	if len(s.Recipes()) != 0 {
		t.Fatal("setup: expected it demoted")
	}
	if n := s.Rearm(); n != 1 {
		t.Errorf("rearmed %d findings, want 1", n)
	}
	if len(s.Recipes()) != 1 {
		t.Error("a rearmed finding did not return to the pool on its surviving wins")
	}
	if s.Collapsed(now, 24*time.Hour, 1) {
		t.Error("rearm left the demotion timestamps behind, so it would collapse again immediately")
	}
}

// A search should start next to something that has held, and the axes are the
// only form that can be mutated — rendered args cannot be turned back into them.
func TestStableSeedPrefersTheStrongestRecord(t *testing.T) {
	now := time.Unix(0, 0)
	s, _ := Open("", func() time.Time { now = now.Add(time.Hour); return now })
	confirmed(t, s, "weak")
	confirmed(t, s, "strong")
	s.Won("strong", []string{"--dpi-desync=fake"}, "social", nil)

	seed, ok := s.StableSeed()
	if !ok {
		t.Fatal("no seed from a store with confirmed findings")
	}
	if seed["method"] != "fake" {
		t.Errorf("seed = %v, want the axes of the strongest record", seed)
	}
	// An unconfirmed finding must not seed anything: that would search around a
	// point that has won exactly once.
	s2, _ := Open("", func() time.Time { return now })
	s2.Won("lead", []string{"--x"}, "youtube", map[string]string{"method": "multisplit"})
	if _, ok := s2.StableSeed(); ok {
		t.Error("an unconfirmed lead was used as a seed")
	}
}
