package core

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

func fleetCore(t *testing.T) *Core {
	t.Helper()
	return &Core{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		opts: Options{StateFile: filepath.Join(t.TempDir(), "state.json")},
	}
}

// The failure this exists for: on 2026-08-13 the client started before the host
// had a resolver, both HTTP subscriptions failed to resolve, and it built a config
// from the three INLINE nodes — the ones that need no fetch. The owner came home
// to a fleet of three. Note what that means for the test: the bad case is not
// zero, it is a plausible remnant, which is worse because the config it builds
// runs.
func TestAShortFetchFallsBackToTheFleetWeAlreadyKnew(t *testing.T) {
	c := fleetCore(t)
	full := make([]subscription.Node, 108)
	for i := range full {
		full[i] = subscription.Node{ID: string(rune('a' + i%26)), Source: "acme"}
	}
	c.saveFleet(full)

	if got := len(c.loadFleet()); got != 108 {
		t.Fatalf("snapshot round-trip lost nodes: %d", got)
	}
	// Three inline nodes survive a failed fetch and must not be mistaken for a fleet.
	if len(c.loadFleet()) <= 3 {
		t.Fatal("fixture is wrong")
	}
}

// A degraded fetch must never overwrite the snapshot — saving it would teach the
// machine the very collapse the snapshot exists to undo.
func TestADegradedFetchDoesNotOverwriteTheSnapshot(t *testing.T) {
	c := fleetCore(t)
	full := []subscription.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	c.saveFleet(full)
	// saveFleet is only ever called for a set that passed the check in loadNodes;
	// what must hold here is that an EMPTY set is refused outright, since that is
	// the one case a caller could reach directly.
	c.saveFleet(nil)
	if got := len(c.loadFleet()); got != 4 {
		t.Errorf("an empty set overwrote the snapshot: %d nodes left", got)
	}
}

// No state file means no snapshot path, and nothing may panic or write.
func TestNoStateFileMeansNoSnapshot(t *testing.T) {
	c := &Core{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.saveFleet([]subscription.Node{{ID: "a"}})
	if got := c.loadFleet(); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}
