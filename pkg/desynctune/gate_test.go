package desynctune

import (
	"testing"
	"time"
)

func TestGateAdmitsOnePassAtATime(t *testing.T) {
	now := time.Unix(0, 0)
	g := &Gate{Now: func() time.Time { return now }}

	rel, ok := g.Begin("youtube")
	if !ok {
		t.Fatal("the first pass was refused")
	}
	// The sandbox is one table and one queue; a second pass would measure
	// through the first.
	if _, ok := g.Begin("discord"); ok {
		t.Error("a second pass started while one held the sandbox")
	}
	rel()
	if g.Running() {
		t.Error("release did not free the sandbox")
	}
}

func TestGateSpacesPassesAndCoolsPerService(t *testing.T) {
	now := time.Unix(0, 0)
	g := &Gate{PerService: time.Hour, Global: 10 * time.Minute, Now: func() time.Time { return now }}

	rel, _ := g.Begin("youtube")
	rel()

	if _, ok := g.Begin("discord"); ok {
		t.Error("another service ran immediately; the global spacing did nothing")
	}
	now = now.Add(11 * time.Minute)
	rel2, ok := g.Begin("discord")
	if !ok {
		t.Fatal("a different service was refused after the global window")
	}
	rel2()

	now = now.Add(11 * time.Minute)
	if _, ok := g.Begin("youtube"); ok {
		t.Error("youtube ran again inside its own cooldown")
	}
	now = now.Add(time.Hour)
	if _, ok := g.Begin("youtube"); !ok {
		t.Error("youtube was still refused after its cooldown expired")
	}
}

// A search that keeps failing is the worst case to hurry, not the one to retry
// without pause.
func TestFailedPassStillCostsItsCooldown(t *testing.T) {
	now := time.Unix(0, 0)
	g := &Gate{PerService: time.Hour, Global: time.Minute, Now: func() time.Time { return now }}
	rel, _ := g.Begin("youtube")
	rel() // as a deferred release would run after a failed pass
	now = now.Add(2 * time.Minute)
	if _, ok := g.Begin("youtube"); ok {
		t.Error("a failed pass did not cost its cooldown")
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	now := time.Unix(0, 0)
	g := &Gate{PerService: time.Hour, Global: time.Minute, Now: func() time.Time { return now }}
	rel, _ := g.Begin("a")
	rel()
	rel() // a defer plus an explicit call must not free someone else's pass
	now = now.Add(2 * time.Minute)
	if _, ok := g.Begin("b"); !ok {
		t.Error("a double release broke the gate")
	}
	if !g.Running() {
		t.Error("the admitted pass is not holding the sandbox")
	}
}
