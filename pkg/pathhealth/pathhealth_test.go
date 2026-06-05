package pathhealth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeDirect returns canned verdicts per (service,position) — stands in for the
// box-direct prober for zapret/direct steps.
type fakeDirect struct{ ok map[int]bool }

func (f fakeDirect) Probe(_ context.Context, service string, position int) events.ProductionVerdict {
	return events.ProductionVerdict{Service: service, Position: position, OK: f.ok[position], RTTms: 100}
}

// fakeNodes returns canned NodeDelay per pool name — stands in for clash.
// mu guards seen: steps fan out concurrently, so multiple goroutines may call in.
type fakeNodes struct {
	mu   sync.Mutex
	up   map[string]int // pool -> delayMs (present = healthy)
	seen []string       // pools queried (to assert vpn steps used NodeDelay)
}

func (f *fakeNodes) NodeDelay(_ context.Context, name, _ string, _ time.Duration) (int, error) {
	f.mu.Lock()
	f.seen = append(f.seen, name)
	f.mu.Unlock()
	if ms, ok := f.up[name]; ok {
		return ms, nil
	}
	return 0, errors.New("node down")
}

type fixedPos int

func (p fixedPos) Position(string) int { return int(p) }

// chain: 0 PREFERRED/zapret, 1 ALT_ZAPRET/zapret, 2 VPN/vpn(pool=vpnA), 3 EMERGENCY/emergency(pool=emg)
func svcWithChain() registry.Service {
	return registry.Service{
		Name: "discord", ProbeType: "http", ProbeTarget: "https://discord.com/api/v9/gateway",
		Chain: []registry.ChainStep{
			{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret},
			{Position: 1, State: registry.StateAltZapret, StrategyClass: strategy.ClassZapret},
			{Position: 2, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpnA"},
			{Position: 3, State: registry.StateEmergency, StrategyClass: strategy.ClassEmergency, StrategyID: "emg"},
		},
	}
}

func newDetector(t *testing.T, direct fakeDirect, nodes *fakeNodes, current int) *Detector {
	t.Helper()
	reg := &registry.Registry{Services: map[string]registry.Service{"discord": svcWithChain()}}
	return &Detector{Reg: reg, Pos: fixedPos(current), Direct: direct, Nodes: nodes, TestURL: "http://gen204", VPNAttempts: 1, Log: quietLog()}
}

// flakyNodes fails the first failUntil calls for a pool, then succeeds — models a
// hysteria node that needs a moment to come up (warm-up).
type flakyNodes struct {
	mu        sync.Mutex
	failUntil int
	calls     map[string]int
}

func (f *flakyNodes) NodeDelay(_ context.Context, name, _ string, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
	if f.calls[name] <= f.failUntil {
		return 0, errors.New("cold (warming up)")
	}
	return 88, nil
}

func TestWarmupRetrySucceedsOnSecondAttempt(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{"discord": svcWithChain()}}
	nodes := &flakyNodes{failUntil: 1} // first attempt fails, second succeeds
	d := &Detector{Reg: reg, Pos: fixedPos(0), Direct: fakeDirect{ok: map[int]bool{0: false, 1: false}},
		Nodes: nodes, TestURL: "http://gen204", VPNAttempts: 2, Log: quietLog()}
	ph := d.probeService(context.Background(), "discord", svcWithChain())
	if !ph.Steps[2].OK { // vpn pool vpnA: cold first, up on retry
		t.Errorf("warm-up: vpn step should be OK on 2nd attempt, got %+v", ph.Steps[2])
	}
	if nodes.calls["vpnA"] != 2 {
		t.Errorf("vpnA attempts = %d, want 2 (retry after cold fail)", nodes.calls["vpnA"])
	}
}

func TestProbeRoutesByClass(t *testing.T) {
	// zapret steps both fail; vpn pool up, emergency down.
	direct := fakeDirect{ok: map[int]bool{0: false, 1: false}}
	nodes := &fakeNodes{up: map[string]int{"vpnA": 90}}
	d := newDetector(t, direct, nodes, 0)

	ph := d.probeService(context.Background(), "discord", svcWithChain())
	if len(ph.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(ph.Steps))
	}
	if ph.Steps[0].OK || ph.Steps[1].OK {
		t.Error("zapret steps should be down (routed to direct prober)")
	}
	if !ph.Steps[2].OK || ph.Steps[2].RTTms != 90 {
		t.Errorf("vpn step should be up via NodeDelay, got %+v", ph.Steps[2])
	}
	if ph.Steps[3].OK {
		t.Error("emergency step should be down (pool not in fakeNodes)")
	}
	// vpn + emergency steps must have used NodeDelay (not the direct prober).
	if len(nodes.seen) != 2 {
		t.Errorf("NodeDelay queried %v, want exactly [vpnA emg]", nodes.seen)
	}
}

func TestBestWorkingPicksLowestTier(t *testing.T) {
	// pos0 zapret down, pos1 zapret UP, pos2 vpn up -> best is pos1 (lowest healthy).
	direct := fakeDirect{ok: map[int]bool{0: false, 1: true}}
	nodes := &fakeNodes{up: map[string]int{"vpnA": 90}}
	d := newDetector(t, direct, nodes, 2) // currently escalated to VPN
	ph := d.probeService(context.Background(), "discord", svcWithChain())
	if got := ph.BestWorking(); got != 1 {
		t.Errorf("BestWorking = %d, want 1 (lowest healthy tier)", got)
	}
	if ph.Current != 2 {
		t.Errorf("Current = %d, want 2", ph.Current)
	}
}

func TestBestWorkingAllDown(t *testing.T) {
	direct := fakeDirect{ok: map[int]bool{0: false, 1: false}}
	nodes := &fakeNodes{up: map[string]int{}} // all pools down
	d := newDetector(t, direct, nodes, 0)
	ph := d.probeService(context.Background(), "discord", svcWithChain())
	if got := ph.BestWorking(); got != -1 {
		t.Errorf("BestWorking = %d, want -1 (all down)", got)
	}
}

func TestScanSkipsUnprobeable(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{
		"ru-direct": {Name: "ru-direct", ProbeTarget: "", Chain: []registry.ChainStep{{State: registry.StateLocked, StrategyClass: strategy.ClassDirect}}},
		"nailed":    {Name: "nailed", ProbeTarget: "http://x", Static: true, Chain: []registry.ChainStep{{}, {}}},
		"oneStep":   {Name: "oneStep", ProbeTarget: "http://x", Chain: []registry.ChainStep{{StrategyClass: strategy.ClassZapret}}},
	}}
	nodes := &fakeNodes{up: map[string]int{}}
	d := &Detector{Reg: reg, Pos: fixedPos(0), Direct: fakeDirect{}, Nodes: nodes, Log: quietLog()}
	if err := d.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	// none are probeable -> no NodeDelay calls
	if len(nodes.seen) != 0 {
		t.Errorf("unprobeable services should be skipped, NodeDelay saw %v", nodes.seen)
	}
}

func TestNextWorkingOracle(t *testing.T) {
	// pos0 zapret down, pos1 zapret down, pos2 vpn up, pos3 emergency up.
	direct := fakeDirect{ok: map[int]bool{0: false, 1: false}}
	nodes := &fakeNodes{up: map[string]int{"vpnA": 90, "emg": 200}}
	d := newDetector(t, direct, nodes, 0)
	if err := d.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	// from pos0, lowest working above is 2 (vpn).
	if got := d.NextWorking("discord", 0); got != 2 {
		t.Errorf("NextWorking(0) = %d, want 2", got)
	}
	// from pos2, lowest working above is 3 (emergency).
	if got := d.NextWorking("discord", 2); got != 3 {
		t.Errorf("NextWorking(2) = %d, want 3", got)
	}
	// nothing above 3.
	if got := d.NextWorking("discord", 3); got != -1 {
		t.Errorf("NextWorking(3) = %d, want -1", got)
	}
	// unknown service (never scanned) -> -1.
	if got := d.NextWorking("nope", 0); got != -1 {
		t.Errorf("NextWorking(unknown) = %d, want -1", got)
	}
}
