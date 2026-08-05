package prospect

import (
	"testing"
	"time"
)

// Without a deliberate re-test a lead never earns its second win: a discovery
// pass generates candidates fresh, so the same strategy may never be offered
// again and the store fills with findings permanently one measurement short.
func TestLeadsAreOfferedOldestFirst(t *testing.T) {
	now := time.Unix(0, 0)
	s, _ := Open("", func() time.Time { now = now.Add(time.Hour); return now })

	s.Won("old", []string{"--a"}, "youtube", nil)
	s.Won("new", []string{"--b"}, "youtube", nil)

	leads := s.Leads()
	if len(leads) != 2 {
		t.Fatalf("got %d leads, want both", len(leads))
	}
	if leads[0].ID != "old" {
		t.Errorf("leads[0] = %q, want the one whose evidence is going stalest", leads[0].ID)
	}

	// A confirmed finding is no longer a lead — re-testing it would spend the
	// window on something already decided.
	s.Won("old", []string{"--a"}, "discord", nil)
	for _, l := range s.Leads() {
		if l.ID == "old" {
			t.Error("a confirmed finding is still offered for verification")
		}
	}
}
