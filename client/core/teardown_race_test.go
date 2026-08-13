package core

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

// The crash of 2026-08-13, 23:52: a gate goroutine dereferenced c.zapExec after a
// Reload had set it to nil, and a nil-pointer panic in a goroutine takes the whole
// process — the client stayed down for eight minutes because systemd does not
// restart on a panic here.
//
// The window is not narrow. A lane pass runs for SECONDS and the post-measurement
// re-check exists precisely because the world moves during it; a Reload rebuilding
// the loop in that gap is ordinary, not exotic. Two guards already existed on this
// path and the latest read had none.
func TestALaneCallWithNoExecutorDoesNotPanic(t *testing.T) {
	c := &Core{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		reg: &registry.Registry{Services: map[string]registry.Service{
			"youtube": {Name: "youtube", VolumeTarget: "https://www.youtube.com/"},
		}},
	}
	// zapExec is nil, exactly as Reload leaves it between tearing the loop down and
	// building the next one.
	if c.zapExec != nil {
		t.Fatal("fixture is wrong: zapExec must be nil")
	}

	svc := c.reg.Services["youtube"]
	// Each of these dereferenced the executor before the fix.
	if cand, verified, measured := c.provenCandidateFor(context.Background(), svc, "", true, true); cand != "" || verified || measured {
		t.Errorf("provenCandidateFor returned %q/%v/%v with no executor", cand, verified, measured)
	}
	if got := c.rankedCandidates("youtube", "", 3); got != nil {
		t.Errorf("rankedCandidates returned %v with no executor", got)
	}
	if ok, measured, why := c.testRecipe(context.Background(), svc, "some-recipe"); ok || measured || why == "" {
		t.Errorf("testRecipe returned ok=%v measured=%v why=%q; it must refuse WITH a reason and claim no measurement", ok, measured, why)
	}
	c.applyCandidate(context.Background(), "youtube", "some-recipe", true, "")
	// Reaching here without a panic is the assertion.
}
