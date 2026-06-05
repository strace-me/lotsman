package strategy

import (
	"path/filepath"
	"testing"
)

func TestLoadDefinitionsMissingFileIsEmpty(t *testing.T) {
	defs, err := LoadDefinitions(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if defs != nil {
		t.Errorf("missing file should yield nil, got %v", defs)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy-catalog.json")
	want := []Definition{
		{ID: "disc-a", Class: ClassZapret, NFQWSArgs: []string{"--dpi-desync=fake", "--dpi-desync-ttl=5"}, BlockTypes: []string{"tcp_reset"}, Notes: "x"},
		{ID: "disc-b", Class: ClassZapret, NFQWSArgs: []string{"--dpi-desync=split2"}},
	}
	if err := SaveDefinitions(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadDefinitions(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0].ID != "disc-a" || len(got[0].NFQWSArgs) != 2 || got[1].ID != "disc-b" {
		t.Errorf("round trip = %v", got)
	}
}

func TestMergeDefinitions(t *testing.T) {
	existing := []Definition{
		{ID: "disc-a", Notes: "old"},
		{ID: "disc-b", Notes: "keep"},
	}
	incoming := []Definition{
		{ID: "disc-a", Notes: "new"}, // refresh
		{ID: "disc-c", Notes: "added"},
	}
	got := MergeDefinitions(existing, incoming)
	if len(got) != 3 {
		t.Fatalf("want 3 merged, got %d: %v", len(got), got)
	}
	// order: existing first (a refreshed, b kept), then new c.
	if got[0].ID != "disc-a" || got[0].Notes != "new" {
		t.Errorf("disc-a should be refreshed in place: %v", got[0])
	}
	if got[1].ID != "disc-b" || got[1].Notes != "keep" {
		t.Errorf("disc-b should be kept: %v", got[1])
	}
	if got[2].ID != "disc-c" {
		t.Errorf("disc-c should be appended: %v", got[2])
	}
}
