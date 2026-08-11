package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewFileStore(path)

	// Missing file -> empty, no error.
	if got := s.Load(); len(got) != 0 {
		t.Fatalf("cold load = %v, want empty", got)
	}

	if err := s.Save(map[string]int{"youtube": 2, "discord": 0}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := s.Load()
	if got["youtube"] != 2 || got["discord"] != 0 {
		t.Errorf("loaded = %v, want youtube=2 discord=0", got)
	}
}

func TestLoadCorruptIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewFileStore(path).Load(); len(got) != 0 {
		t.Errorf("corrupt load = %v, want empty (best-effort)", got)
	}
}

// Positions are per-network for the same reason the knowledge base is: a position
// answers "which rung works HERE". One shared number made the rung a laptop ended
// on at the office the rung it started from at home, and the walk to the right one
// was paid in broken service on every arrival (LOT-62).
func TestPathForNamesTheNetworkWithoutLosingTheExtension(t *testing.T) {
	cases := []struct{ base, net, want string }{
		{"/var/lib/lotsman/state.json", "86bddb090a8a", "/var/lib/lotsman/state.86bddb090a8a.json"},
		{"state.json", "abc", "state.abc.json"},
		// No network yet (detection failed, or the shared store is deliberate):
		// keep the base path rather than invent a file nobody will look in.
		{"/var/lib/lotsman/state.json", "", "/var/lib/lotsman/state.json"},
		{"", "abc", ""},
		// A base without an extension still has to yield distinct per-network paths.
		{"/var/lib/lotsman/state", "abc", "/var/lib/lotsman/state.abc"},
	}
	for _, c := range cases {
		if got := PathFor(c.base, c.net); got != c.want {
			t.Errorf("PathFor(%q, %q) = %q, want %q", c.base, c.net, got, c.want)
		}
	}
}

// Two networks must not read each other's rungs — the whole point.
func TestPerNetworkStoresDoNotSeeEachOther(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "state.json")
	home := NewFileStore(PathFor(base, "home"))
	office := NewFileStore(PathFor(base, "office"))
	if err := home.Save(map[string]int{"youtube": 0}); err != nil {
		t.Fatal(err)
	}
	if err := office.Save(map[string]int{"youtube": 3}); err != nil {
		t.Fatal(err)
	}
	if got := home.Load()["youtube"]; got != 0 {
		t.Errorf("home saw position %d, want 0 — the office store leaked into it", got)
	}
	if got := office.Load()["youtube"]; got != 3 {
		t.Errorf("office saw position %d, want 3", got)
	}
}
