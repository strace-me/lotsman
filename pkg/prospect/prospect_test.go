package prospect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storeAt(t *testing.T, path string) *Store {
	t.Helper()
	n := time.Unix(0, 0)
	s, err := Open(path, func() time.Time { n = n.Add(time.Minute); return n })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// One win is a lead, not a recipe: DPI drifts, and a single measurement taken in
// one minute can be luck. Putting leads into production is how an unverified
// strategy reaches the household.
func TestOneWinIsALeadAndTwoIsARecipe(t *testing.T) {
	s := storeAt(t, "")
	args := []string{"--dpi-desync=fake,multisplit"}

	s.Won("abc", args, "youtube", map[string]string{"method": "fake"})
	if got := s.Recipes(); len(got) != 0 {
		t.Errorf("a single win produced %d recipes; it must produce none", len(got))
	}
	if s.Pending() != 1 {
		t.Errorf("pending = %d, want the lead counted", s.Pending())
	}

	s.Won("abc", args, "discord", nil)
	got := s.Recipes()
	if len(got) != 1 {
		t.Fatalf("two wins produced %d recipes, want 1", len(got))
	}
	if !strings.Contains(got[0].Provenance, "discord") || !strings.Contains(got[0].Provenance, "youtube") {
		t.Errorf("provenance loses which services proved it: %q", got[0].Provenance)
	}
	if s.Pending() != 0 {
		t.Error("a confirmed finding is still counted as pending")
	}
}

// What worked in June is routinely dead by August, so a confirmed strategy that
// starts losing must leave the pool again.
func TestConfirmedStrategyIsDemotedWhenItStopsWorking(t *testing.T) {
	s := storeAt(t, "")
	s.Won("abc", []string{"--dpi-desync=fake"}, "youtube", nil)
	s.Won("abc", []string{"--dpi-desync=fake"}, "youtube", nil)
	if len(s.Recipes()) != 1 {
		t.Fatal("setup: not confirmed")
	}
	for i := 0; i < demoteAt; i++ {
		s.Lost("abc")
	}
	if got := s.Recipes(); len(got) != 0 {
		t.Errorf("a strategy that lost %d times is still in the pool", demoteAt)
	}
}

// The value here accumulates over weeks; a restart must not cost it.
func TestFindingsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prospect.json")
	s := storeAt(t, path)
	s.Won("abc", []string{"--dpi-desync=fake"}, "youtube", nil)
	s.Won("abc", []string{"--dpi-desync=fake"}, "youtube", nil)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again := storeAt(t, path)
	if got := again.Recipes(); len(got) != 1 || got[0].ID != "self-abc" {
		t.Fatalf("reopened store has %v, want the confirmed finding", got)
	}
}

// Silently starting fresh would discard weeks of evidence and look like a box
// that has simply never found anything.
func TestCorruptStoreRefusesRatherThanResetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prospect.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("a corrupt store was silently replaced with an empty one")
	}
}

// The composer diffs the argv it composes; a set that reorders itself between
// passes would read as a strategy change and restart the engine for nothing.
func TestRecipeOrderIsStable(t *testing.T) {
	s := storeAt(t, "")
	for _, id := range []string{"zzz", "aaa", "mmm"} {
		s.Won(id, []string{"--dpi-desync=fake"}, "svc", nil)
		s.Won(id, []string{"--dpi-desync=fake"}, "svc", nil)
	}
	first := s.Recipes()
	for i := 0; i < 5; i++ {
		next := s.Recipes()
		for j := range first {
			if first[j].ID != next[j].ID {
				t.Fatalf("recipe order changed between calls at %d: %s vs %s", j, first[j].ID, next[j].ID)
			}
		}
	}
}
