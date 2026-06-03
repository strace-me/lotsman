package brain

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// twoStepReg builds a youtube service with a 2-step chain and the given
// per-service overrides, for the static/threshold tests.
func twoStepReg(static bool, escalateAfter int) *registry.Registry {
	return &registry.Registry{Services: map[string]registry.Service{
		"youtube": {
			Name: "youtube", Static: static, EscalateAfter: escalateAfter,
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret, StrategyID: "alt12"},
				{Position: 1, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		},
	}}
}

func expectNoDesired(t *testing.T, bus *events.Bus, dur time.Duration) {
	t.Helper()
	select {
	case d := <-bus.DesiredState:
		t.Fatalf("expected no desired change, got %+v", d)
	case <-time.After(dur):
	}
}

func TestStaticServiceNeverEscalates(t *testing.T) {
	bus := events.NewBus()
	b := New(bus, twoStepReg(true, 0), fakeKB{alt: "alt10"}, Config{EscalateFails: 3, SettlingWindow: 0}, nil, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	readDesired(t, bus) // init -> pos 0
	for i := 0; i < 6; i++ {
		sendVerdict(bus, 0, false) // far past the global threshold
	}
	expectNoDesired(t, bus, 200*time.Millisecond) // nailed: holds position
}

func TestPerServiceEscalateAfter(t *testing.T) {
	bus := events.NewBus()
	// per-service threshold 2 overrides the global 3.
	b := New(bus, twoStepReg(false, 2), fakeKB{alt: "alt10"}, Config{EscalateFails: 3, SettlingWindow: 0}, nil, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	readDesired(t, bus) // init -> pos 0
	sendVerdict(bus, 0, false)
	sendVerdict(bus, 0, false) // 2 fails == per-service threshold
	if d := readDesired(t, bus); d.Position != 1 {
		t.Fatalf("escalate after 2 fails: got pos=%d, want 1", d.Position)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeKB resolves any runtime-chosen zapret step to a fixed strategy.
type fakeKB struct{ alt string }

func (f fakeKB) TopNZapret(_ string, _ int, _ ...string) []string { return []string{f.alt} }

// readDesired pulls the next DesiredStateChanged or fails on timeout.
func readDesired(t *testing.T, bus *events.Bus) events.DesiredStateChanged {
	t.Helper()
	select {
	case d := <-bus.DesiredState:
		return d
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DesiredStateChanged")
		return events.DesiredStateChanged{}
	}
}

func sendVerdict(bus *events.Bus, pos int, ok bool) {
	bus.Verdicts <- events.ProductionVerdict{Service: "youtube", Position: pos, OK: ok}
}

// TestEscalateThenRecover drives the state machine through the full loop with
// settling disabled so the test is deterministic and fast.
func TestEscalateThenRecover(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	// init -> PREFERRED (pos 0)
	if d := readDesired(t, bus); d.Position != 0 || d.State != registry.StatePreferred {
		t.Fatalf("init: got pos=%d state=%s, want 0/PREFERRED", d.Position, d.State)
	}

	// 3 active failures -> escalate to ALT_ZAPRET (pos 1), strategy from KB.
	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	d := readDesired(t, bus)
	if d.Position != 1 || d.State != registry.StateAltZapret || d.StrategyID != "alt10" {
		t.Fatalf("escalate1: got pos=%d state=%s strat=%s, want 1/ALT_ZAPRET/alt10", d.Position, d.State, d.StrategyID)
	}

	// 3 more active failures at pos 1 -> escalate to VPN (pos 2).
	for i := 0; i < 3; i++ {
		sendVerdict(bus, 1, false)
	}
	if d := readDesired(t, bus); d.Position != 2 || d.State != registry.StateVPN {
		t.Fatalf("escalate2: got pos=%d state=%s, want 2/VPN", d.Position, d.State)
	}

	// Fewer than RecoverSuccess silent successes at pos 0 must NOT recover.
	for i := 0; i < 4; i++ {
		sendVerdict(bus, 0, true)
	}
	select {
	case d := <-bus.DesiredState:
		t.Fatalf("recovered too early after 4 successes: pos=%d", d.Position)
	case <-time.After(100 * time.Millisecond):
	}

	// 5th silent success -> recover straight to PREFERRED (pos 0).
	sendVerdict(bus, 0, true)
	if d := readDesired(t, bus); d.Position != 0 || d.State != registry.StatePreferred {
		t.Fatalf("recover: got pos=%d state=%s, want 0/PREFERRED", d.Position, d.State)
	}
}

// TestSmartsSystemicSuppressesEscalation: with the intelligence layer wired and
// a systemic outage, repeated active failures must NOT escalate (don't churn the
// chain when the whole upstream is down).
func TestSmartsSystemicSuppressesEscalation(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	b.SetSmarts(&Smarts{
		Threshold: func(string, string) int { return 3 },
		Systemic:  func() bool { return true }, // everything is down
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init pos 0

	for i := 0; i < 6; i++ { // well past threshold
		sendVerdict(bus, 0, false)
	}
	select {
	case d := <-bus.DesiredState:
		t.Fatalf("escalated during systemic outage to pos %d (should hold)", d.Position)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestSmartsEscalatesWhenLocal: same wiring but NOT systemic -> escalates at
// the adaptive threshold like normal.
func TestSmartsEscalatesWhenLocal(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	b.SetSmarts(&Smarts{
		Threshold: func(string, string) int { return 3 },
		Systemic:  func() bool { return false },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init

	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	if d := readDesired(t, bus); d.Position != 1 {
		t.Fatalf("local failure should escalate to pos 1, got %d", d.Position)
	}
}

// TestSmartsBlockTypeJump: when the detected block points at VPN, escalation
// jumps straight to the VPN chain step, skipping the intermediate zapret one.
func TestSmartsBlockTypeJump(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin() // youtube: PREFERRED zapret, ALT_ZAPRET zapret, VPN
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	b.SetSmarts(&Smarts{
		Threshold:    func(string, string) int { return 3 },
		Systemic:     func() bool { return false },
		SuggestClass: func(string, int) string { return "vpn" }, // IP-block => go VPN
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init pos 0 (zapret)

	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	d := readDesired(t, bus)
	if d.Position != 2 || d.State != registry.StateVPN {
		t.Fatalf("block-type jump: got pos=%d state=%s, want 2/VPN (skip ALT_ZAPRET)", d.Position, d.State)
	}
}

// TestRestoreResumesPosition verifies persisted positions are honored at start:
// a restored service emits its restored state on init, not PREFERRED.
func TestRestoreResumesPosition(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	b := New(bus, reg, fakeKB{alt: "alt10"}, DefaultConfig(), nil, discardLogger())
	b.Restore(map[string]int{"youtube": 2}) // VPN

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	if d := readDesired(t, bus); d.Position != 2 || d.State != registry.StateVPN {
		t.Fatalf("restore: init emitted pos=%d state=%s, want 2/VPN", d.Position, d.State)
	}
}

// TestPersistHookFiresOnTransition verifies the persist callback receives
// updated positions when state changes.
func TestPersistHookFiresOnTransition(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())

	saved := make(chan map[string]int, 8)
	b.SetPersist(func(p map[string]int) { saved <- p })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init (also triggers a persist)
	<-saved             // drain init persist

	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	readDesired(t, bus) // escalation
	select {
	case p := <-saved:
		if p["youtube"] != 1 {
			t.Errorf("persisted youtube=%d, want 1 after escalation", p["youtube"])
		}
	case <-time.After(time.Second):
		t.Fatal("persist hook did not fire on transition")
	}
}

// TestFailuresDuringSettlingIgnored verifies the settling window suppresses
// escalation right after a switch.
func TestFailuresDuringSettlingIgnored(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: time.Hour}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init

	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	readDesired(t, bus) // escalate to pos 1, now in a 1h settling window

	// Failures at the new active position during settling must be ignored.
	for i := 0; i < 5; i++ {
		sendVerdict(bus, 1, false)
	}
	select {
	case d := <-bus.DesiredState:
		t.Fatalf("escalated during settling window: pos=%d", d.Position)
	case <-time.After(100 * time.Millisecond):
	}
}
