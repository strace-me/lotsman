package core

import (
	"context"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

// Twenty-two candidates for youtube came back with one identical sentence — "did
// not complete at all — no bytes, no response" — and from outside that has two
// causes that look the same: the censor kills the handshake whatever we send, or
// our profile never touched the traffic. Every rejection now carries what the
// SAME lane measured with nothing on the queue, so the two can be told apart at
// the moment the verdict is written instead of inferred later from two log lines.
func TestEveryRejectionCarriesTheNoDesyncControl(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	svc := c.reg.Services["youtube"]

	var sawBaseline bool
	c.testCandidate = func(_ context.Context, _ registry.Service, id string) (bool, bool, string) {
		return false, true, "did not complete at all"
	}
	// The control arm goes through the same seam; record that it was asked for.
	c.testBaselineFn = func(context.Context, registry.Service) (bool, bool, string) {
		sawBaseline = true
		return false, true, "did not complete at all"
	}
	cand, _, measured := c.provenCandidateFor(context.Background(), svc, "", false, false)
	if cand != "" {
		t.Fatalf("nothing carried; nothing should be crowned, got %q", cand)
	}
	if !measured {
		t.Error("the candidates were measured and failed; that is a measurement")
	}
	if !sawBaseline {
		t.Fatal("the pass never took a control measurement")
	}
}

// A control that CARRIES is the loudest possible result: the path was not broken,
// so no recipe is what this rule needs, and a candidate "passing" here would be
// taking credit for a path nothing was wrong with.
func TestABaselineThatCarriesIsSaidOutLoud(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	svc := c.reg.Services["youtube"]
	c.testBaselineFn = func(context.Context, registry.Service) (bool, bool, string) {
		return true, true, "tcp carried 64 KiB at 300 KiB/s"
	}
	got := c.baseline(context.Background(), svc)
	if !strings.Contains(got, "CARRIES WITHOUT DESYNC") {
		t.Errorf("a carrying control must be unmissable in the record, got %q", got)
	}
}
