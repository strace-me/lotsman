package probing

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeProber records every (service, position) it is asked to probe and returns
// a fixed verdict. Calls are guarded by a mutex so the test goroutine can read
// them without racing the engine goroutine.
type fakeProber struct {
	mu     sync.Mutex
	probes []struct {
		service string
		pos     int
	}
	ok  bool
	rtt int
}

func (p *fakeProber) Probe(_ context.Context, service string, position int) events.ProductionVerdict {
	p.mu.Lock()
	p.probes = append(p.probes, struct {
		service string
		pos     int
	}{service, position})
	p.mu.Unlock()
	return events.ProductionVerdict{Service: service, Position: position, OK: p.ok, RTTms: p.rtt}
}

func (p *fakeProber) positions(service string) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []int
	for _, pr := range p.probes {
		if pr.service == service {
			out = append(out, pr.pos)
		}
	}
	return out
}

// fixedPositioner reports a constant position for every service. It stands in
// for Brain, which the engine consults but does not drive.
type fixedPositioner int

func (f fixedPositioner) Position(string) int { return int(f) }

// oneServiceReg is a registry with a single zapret-chained service so position
// 0/1 map to known strategy IDs.
func oneServiceReg() *registry.Registry {
	return &registry.Registry{Services: map[string]registry.Service{
		"youtube": {
			Name:        "youtube",
			ProbeTarget: "https://www.youtube.com/generate_204",
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret, StrategyID: "alt12"},
				{Position: 1, State: registry.StateAltZapret, StrategyClass: strategy.ClassZapret, StrategyID: "alt11"},
				{Position: 2, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		},
	}}
}

// TestActiveProbePublishesVerdict: one tick at position 0 probes the active
// position and publishes a verdict to the bus.
func TestActiveProbePublishesVerdict(t *testing.T) {
	bus := events.NewBus()
	prober := &fakeProber{ok: true, rtt: 42}
	e := New(bus, prober, fixedPositioner(0), oneServiceReg(), kb.New(), nil, nil, 5*time.Millisecond, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	select {
	case v := <-bus.Verdicts:
		if v.Service != "youtube" || v.Position != 0 || !v.OK || v.RTTms != 42 {
			t.Fatalf("verdict = %+v, want youtube/pos0/ok/42", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no verdict published")
	}

	if got := prober.positions("youtube"); len(got) == 0 || got[0] != 0 {
		t.Fatalf("active probe positions = %v, want first probe at 0", got)
	}
}

// LOT-43: a header-only prober can't see the TSPU throttle freeze, so when the
// stall oracle reports the service stalled, a "healthy" ACTIVE probe is overridden
// to a failure — driving Brain to escalate off the throttled path.
func TestStallOracleFailsActiveProbe(t *testing.T) {
	bus := events.NewBus()
	prober := &fakeProber{ok: true, rtt: 42}
	e := New(bus, prober, fixedPositioner(0), oneServiceReg(), kb.New(), nil, nil, 5*time.Millisecond, discardLog())
	e.SetStallOracle(func(string) (string, bool) { return "flows frozen mid-stream", true })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	select {
	case v := <-bus.Verdicts:
		if v.OK {
			t.Fatalf("stalled service's active probe must be failed; got OK verdict %+v", v)
		}
		if v.Err == "" {
			t.Error("overridden verdict should carry a throttle-stall err")
		}
	case <-time.After(time.Second):
		t.Fatal("no verdict published")
	}
}

// TestRecordOutcomeInvoked: the engine folds the probed strategy's outcome into
// the KB. A successful probe at position 0 must move youtube|alt12 above the
// 0.5 prior and mark it seen.
func TestRecordOutcomeInvoked(t *testing.T) {
	bus := events.NewBus()
	prober := &fakeProber{ok: true, rtt: 30}
	k := kb.New()
	e := New(bus, prober, fixedPositioner(0), oneServiceReg(), k, nil, nil, 5*time.Millisecond, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Drain the verdict to confirm the tick completed (RecordOutcome runs before
	// the publish in runProbe).
	select {
	case <-bus.Verdicts:
	case <-time.After(time.Second):
		t.Fatal("no verdict published")
	}

	s := k.Stats("youtube", "alt12")
	if !s.Seen {
		t.Fatalf("KB has no record for youtube|alt12; RecordOutcome was not invoked")
	}
	if s.Success <= 0.5 {
		t.Fatalf("success EWMA = %v after a success, want > prior 0.5", s.Success)
	}
}

// observeRec records ObserveProbe callbacks (mutex-guarded).
type observeRec struct {
	mu    sync.Mutex
	count int
}

func (o *observeRec) ObserveProbe(string, bool, int) {
	o.mu.Lock()
	o.count++
	o.mu.Unlock()
}

func (o *observeRec) calls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.count
}

// TestSilentRecoveryRotatesLowerPosition: when a service sits above position 0,
// each tick additionally probes a lower (preferred) position so Brain can detect
// recovery. At position 2 the engine probes the active 2 plus a lower position
// (0 then 1 as the rotation advances).
func TestSilentRecoveryRotatesLowerPosition(t *testing.T) {
	bus := events.NewBus()
	prober := &fakeProber{ok: true, rtt: 10}
	obs := &observeRec{}
	e := New(bus, prober, fixedPositioner(2), oneServiceReg(), kb.New(), obs, nil, 5*time.Millisecond, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Two ticks => 2 probes each (active + silent). Drain 4 verdicts to be sure
	// both ticks have fully run.
	deadline := time.After(2 * time.Second)
	for got := 0; got < 4; {
		select {
		case <-bus.Verdicts:
			got++
		case <-deadline:
			t.Fatalf("only %d verdicts after two ticks", got)
		}
	}
	cancel()

	positions := prober.positions("youtube")
	var sawActive, sawLower bool
	for _, p := range positions {
		switch {
		case p == 2:
			sawActive = true
		case p < 2:
			sawLower = true
		}
	}
	if !sawActive {
		t.Errorf("never probed the active position 2; positions=%v", positions)
	}
	if !sawLower {
		t.Errorf("never probed a lower (silent recovery) position; positions=%v", positions)
	}
	if obs.calls() == 0 {
		t.Errorf("ProbeObserver.ObserveProbe was never called")
	}
}
