package aggregate

import (
	"strings"
	"testing"
)

func TestMergeOwnKeepsTheOperatorsEntries(t *testing.T) {
	got, added := mergeOwn([]string{"b.com", "c.com"}, []string{"A.com", "c.com", "*.d.com"})
	want := []string{"a.com", "b.com", "c.com", "d.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("merged = %v, want %v", got, want)
	}
	// c.com was already there; a.com and d.com were not.
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}
}

// Junk typed into the app must be held to the same standard as junk fetched from
// a URL, or the app becomes the way to get a bad entry into a list.
func TestMergeOwnDropsJunkTheSameWayAFetchWould(t *testing.T) {
	got, _ := mergeOwn(nil, []string{"good.com", "not a domain", "# a comment", ""})
	if strings.Join(got, ",") != "good.com" {
		t.Errorf("merged = %v, want just good.com", got)
	}
}

func TestMergeOwnIsANoopWithoutOwnEntries(t *testing.T) {
	in := []string{"b.com", "a.com"}
	got, added := mergeOwn(in, nil)
	// Unsorted on purpose: with nothing of our own to add there is nothing to
	// reorder, and re-sorting would rewrite the file for no reason.
	if strings.Join(got, ",") != "b.com,a.com" || added != 0 {
		t.Errorf("got %v (added %d), want the input untouched", got, added)
	}
}
