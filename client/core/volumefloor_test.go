package core

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

// The floor answered the wrong question. A frozen path and a slow path are not
// the same failure, and the freeze is the one this whole seam exists for — a
// frozen read is exactly one that delivered LESS than it asked for, and that fact
// was already in the reading with nothing looking at it.
//
// The default 64 KiB/s was chosen when the ask was 64 KiB: "the whole thing in
// about a second". Applied unchanged to a 24 KiB ask it demands the lot in 0.4s.
// Measured on the owner's laptop: three candidates for `x` delivered their full
// 24 KiB at 7, 21 and 36 KiB/s — the best plainly better than an incumbent that
// freezes at 16 KiB and never finishes — and all three were rejected.
func TestVolumeFloorFollowsTheAsk(t *testing.T) {
	c := &Core{}
	c.opts.CanaryGoodputKBps = 64

	// The original shape: a 64 KiB ask still demands 64 KiB/s.
	if got := c.volumeFloor(registry.Service{}, 64<<10); got != 64 {
		t.Errorf("64 KiB ask: floor %v, want 64", got)
	}
	// A smaller ask cannot demand a speed it is too small to show. 24 KiB in one
	// second is 24 KiB/s, so the 36 KiB/s candidate passes and the 21 does not.
	if got := c.volumeFloor(registry.Service{}, 24576); got != 24 {
		t.Errorf("24 KiB ask: floor %v, want 24", got)
	}
	// It never demands MORE than the operator configured.
	c.opts.CanaryGoodputKBps = 16
	if got := c.volumeFloor(registry.Service{}, 64<<10); got != 16 {
		t.Errorf("a lower global must cap the derivation, got %v", got)
	}
	// And a rule's own figure wins outright.
	c.opts.CanaryGoodputKBps = 64
	if got := c.volumeFloor(registry.Service{VolumeFloorKBps: 5}, 64<<10); got != 5 {
		t.Errorf("per-rule floor ignored, got %v", got)
	}
}
