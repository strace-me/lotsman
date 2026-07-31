package brain

import (
	"context"
	"log/slog"
	"slices"
	"sort"
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

// fakeOracle is a canned PathOracle: next[above] = lowest working position above
// (E-2 escalation), rec[below] = lowest stably-healthy position below (E-4
// recovery); missing => -1.
type fakeOracle struct {
	next map[int]int
	rec  map[int]int
}

func (f fakeOracle) NextWorking(_ string, above int) int {
	if n, ok := f.next[above]; ok {
		return n
	}
	return -1
}

func (f fakeOracle) RecoverTarget(_ string, below int) int {
	if r, ok := f.rec[below]; ok {
		return r
	}
	return -1
}

// TestPathOracleParallelRecovery: escalation-v2 E-4 — Brain recovers straight to
// the best lower tier (PREFERRED, pos 0) from VPN (pos 2), not one rung at a time.
func TestPathOracleParallelRecovery(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin() // youtube: 0 zapret, 1 zapret, 2 vpn
	b := New(bus, reg, fakeKB{alt: "alt10"}, DefaultConfig(), nil, discardLogger())
	b.Restore(map[string]int{"youtube": 2})             // escalated to VPN
	b.SetPathOracle(fakeOracle{rec: map[int]int{2: 0}}) // from pos2, pos0 is stably healthy

	b.recoverViaOracle()
	d := readDesired(t, bus)
	if d.Position != 0 || d.State != registry.StatePreferred {
		t.Fatalf("E-4 recovery: got pos=%d state=%s, want 0/PREFERRED (best lower tier)", d.Position, d.State)
	}
}

// E-4 must not flap: a service in damper backoff is NOT recovered, even when the
// (box-direct, possibly over-optimistic) oracle says a lower tier is healthy.
func TestPathOracleRecoverySuppressedByDamper(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	b := New(bus, reg, fakeKB{alt: "alt10"}, DefaultConfig(), nil, discardLogger())
	b.Restore(map[string]int{"youtube": 2})
	b.SetPathOracle(fakeOracle{rec: map[int]int{2: 0}})
	b.SetSmarts(&Smarts{FlapBackoff: func(string, time.Time) time.Duration { return time.Minute }})

	b.recoverViaOracle()
	if got := b.Position("youtube"); got != 2 {
		t.Fatalf("recovery must be suppressed during damper backoff, position=%d want 2", got)
	}
}

// TestPathOracleJumpsToBestWorkingTier: escalation-v2 E-2 — Brain escalates
// straight to the empirically-working tier (VPN, pos 2), skipping ALT_ZAPRET.
func TestPathOracleJumpsToBestWorkingTier(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin() // youtube: 0 zapret, 1 zapret(alt), 2 vpn
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	b.SetPathOracle(fakeOracle{next: map[int]int{0: 2}}) // from pos0 only VPN(2) works

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init pos 0

	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	d := readDesired(t, bus)
	if d.Position != 2 || d.State != registry.StateVPN {
		t.Fatalf("path-health jump: got pos=%d state=%s, want 2/VPN (skip ALT_ZAPRET)", d.Position, d.State)
	}
}

// TestPathOracleNoDataFallsBackToOneRung: with no oracle data (-1), escalation
// is the original one-rung step (pos 0 -> 1).
func TestPathOracleNoDataFallsBackToOneRung(t *testing.T) {
	bus := events.NewBus()
	reg := registry.Builtin()
	cfg := Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 0}
	b := New(bus, reg, fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	b.SetPathOracle(fakeOracle{next: map[int]int{}}) // always -1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	readDesired(t, bus) // init pos 0

	for i := 0; i < 3; i++ {
		sendVerdict(bus, 0, false)
	}
	d := readDesired(t, bus)
	if d.Position != 1 {
		t.Fatalf("no oracle data: got pos=%d, want 1 (one-rung fallback)", d.Position)
	}
}

// threeStepReg builds a youtube service with the canonical 3-rung chain
// (PREFERRED zapret -> ALT_ZAPRET zapret -> VPN), so escalation from pos 1 to
// pos 2 is observable — distinguishing "settling gated it" from "end of chain".
func threeStepReg() *registry.Registry {
	return &registry.Registry{Services: map[string]registry.Service{
		"youtube": {
			Name: "youtube",
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret, StrategyID: "alt12"},
				{Position: 1, State: registry.StateAltZapret, StrategyClass: strategy.ClassZapret},
				{Position: 2, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		},
	}}
}

// LOT-13 (audit §4): a freshly-switched strategy gets a settling window during
// which active-position failures are IGNORED (don't immediately re-escalate the
// rung we just moved to). EscalateFails:1 makes an un-gated fail escalate at once,
// so the absence of a climb to pos 2 proves the gate. Positive control:
// TestEscalateThenRecover runs the same climb with SettlingWindow:0 and DOES climb.
func TestSettlingWindowIgnoresActiveFailures(t *testing.T) {
	bus := events.NewBus()
	cfg := Config{EscalateFails: 1, RecoverSuccess: 5, SettlingWindow: time.Minute}
	b := New(bus, threeStepReg(), fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	readDesired(t, bus) // init pos 0 (settlingUntil zero -> not gated)
	sendVerdict(bus, 0, false)
	if d := readDesired(t, bus); d.Position != 1 {
		t.Fatalf("first fail must escalate pos0->1, got %d", d.Position)
	}
	// At pos 1 the settling window is open. Active-position fails are ignored.
	for i := 0; i < 3; i++ {
		sendVerdict(bus, 1, false)
	}
	expectNoDesired(t, bus, 200*time.Millisecond)
}

// LOT-13 (audit §4): the settling window gates ONLY the active position. Silent
// recovery probes a LOWER (preferred) position, so they are NOT gated — recovery
// proceeds even while the just-switched rung is still settling.
func TestSettlingDoesNotGateSilentRecovery(t *testing.T) {
	bus := events.NewBus()
	cfg := Config{EscalateFails: 1, RecoverSuccess: 2, SettlingWindow: time.Minute}
	b := New(bus, threeStepReg(), fakeKB{alt: "alt10"}, cfg, nil, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	readDesired(t, bus) // init pos 0
	sendVerdict(bus, 0, false)
	if d := readDesired(t, bus); d.Position != 1 {
		t.Fatalf("escalate pos0->1: got %d", d.Position)
	}
	// Settling open at pos 1; silent successes at the lower pos 0 still accumulate.
	sendVerdict(bus, 0, true)
	sendVerdict(bus, 0, true) // == RecoverSuccess
	if d := readDesired(t, bus); d.Position != 0 {
		t.Fatalf("silent recovery must proceed during settling: got pos=%d, want 0", d.Position)
	}
}

// LOT-13 (audit §4): reassert is a non-blocking re-converge nudge. When the
// applier is busy (DesiredState saturated) the nudge is DROPPED and retried next
// tick — it must never block the Brain's Run loop.
func TestReassertDropsUnderBackpressure(t *testing.T) {
	bus := events.NewBus()
	b := New(bus, twoStepReg(false, 3), fakeKB{alt: "alt10"}, Config{EscalateFails: 3}, nil, discardLogger())

	// Saturate DesiredState so the applier looks busy.
	filled := 0
	for {
		select {
		case bus.DesiredState <- events.DesiredStateChanged{Service: "filler"}:
			filled++
			continue
		default:
		}
		break
	}
	if filled == 0 {
		t.Fatal("DesiredState has no buffer to saturate")
	}

	done := make(chan struct{})
	go func() { b.Reassert(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reassert blocked under backpressure (must drop, not block)")
	}

	// Nothing new was enqueued — the youtube reassert was dropped, all slots fillers.
	if got := len(bus.DesiredState); got != filled {
		t.Errorf("reassert enqueued under backpressure (len=%d, want %d): drop not honored", got, filled)
	}
	for i := 0; i < filled; i++ {
		if d := <-bus.DesiredState; d.Service != "filler" {
			t.Errorf("found %q in queue — reassert was not dropped", d.Service)
		}
	}
}

// LOT-40: preferBlockType floats strategies declared to beat the observed block
// type to the front, keeping EWMA order within the matched/unmatched groups.
func TestPreferBlockType(t *testing.T) {
	lookup := func(id string) []string {
		return map[string][]string{"alt12": {"tcp_reset"}, "alt11": {"timeout"}}[id] // alt10 -> nil
	}
	// EWMA order alt11,alt10,alt12; tcp_reset observed -> alt12 floats first.
	if got := preferBlockType([]string{"alt11", "alt10", "alt12"}, "tcp_reset", lookup); got[0] != "alt12" {
		t.Errorf("tcp_reset must float alt12 first, got %v", got)
	}
	// stable within groups (match first, rest keep order).
	if got := preferBlockType([]string{"alt11", "alt10", "alt12"}, "timeout", lookup); got[0] != "alt11" || got[1] != "alt10" || got[2] != "alt12" {
		t.Errorf("timeout: want [alt11 alt10 alt12], got %v", got)
	}
	// no match (or no observed type) -> unchanged EWMA order.
	if got := preferBlockType([]string{"alt11", "alt10"}, "throttle", lookup); got[0] != "alt11" || got[1] != "alt10" {
		t.Errorf("no match must keep EWMA order, got %v", got)
	}
}

// Snapshot must be ordered: runtimes is a map, and Go randomises map iteration on
// every call. Callers treat the result as a stable list — the desync composer turns it
// into an ordered set of nfqws --new blocks — so a reshuffle made the composed argv
// differ every tick, which made Apply kill and relaunch nfqws continuously.
func TestSnapshotIsDeterministicallyOrdered(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{}}
	for _, name := range []string{"zeta", "alpha", "mike", "delta"} {
		reg.Services[name] = registry.Service{
			Name: name,
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret, StrategyID: "alt12"},
				{Position: 1, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		}
	}
	b := New(events.NewBus(), reg, fakeKB{alt: "alt10"}, DefaultConfig(), nil, discardLogger())

	var names []string
	for _, s := range b.Snapshot() {
		names = append(names, s.Service)
	}
	if len(names) != 4 {
		t.Fatalf("snapshot has %d services, want 4", len(names))
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("snapshot must be sorted by service, got %v", names)
	}
	// Repeat: map order varies per call, so an unsorted implementation fails here fast.
	for i := 0; i < 20; i++ {
		var again []string
		for _, s := range b.Snapshot() {
			again = append(again, s.Service)
		}
		if !slices.Equal(names, again) {
			t.Fatalf("snapshot order changed between calls: %v then %v", names, again)
		}
	}
}
