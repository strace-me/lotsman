package brain

import (
	"sync"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// registryWithN builds a registry of n services, each a two-step zapret→vpn chain
// with escalateAfter, so a few bad verdicts push every service to escalate.
func registryWithN(n int, escalateAfter int) *registry.Registry {
	svcs := make(map[string]registry.Service, n)
	for i := 0; i < n; i++ {
		name := "svc-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		svcs[name] = registry.Service{
			Name: name, EscalateAfter: escalateAfter,
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret, StrategyID: "alt12"},
				{Position: 1, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		}
	}
	return &registry.Registry{Services: svcs}
}

// TestEmitDesiredLockedReleasesMu reproduces the core of LOT-75: when the
// DesiredState channel is full, a blocking send must not hold b.mu — otherwise the
// entire policy loop stalls. The test fills the channel, invokes
// emitDesiredLocked (which blocks on the send), and verifies a concurrent
// acquirer can take b.mu while that send is parked.
func TestEmitDesiredLockedReleasesMu(t *testing.T) {
	reg := registryWithN(2, 2)
	bus := events.NewBus()
	b := New(bus, reg, fakeKB{alt: "alt10"}, Config{EscalateFails: 3, SettlingWindow: 0}, nil, discardLogger())

	// Fill the DesiredState channel so the next send blocks.
	name := ""
	for n := range reg.Services {
		name = n
		break
	}
	rt := b.runtimes[name]
	for i := 0; i < cap(bus.DesiredState); i++ {
		bus.DesiredState <- events.DesiredStateChanged{}
	}

	// Drive emitDesiredLocked in a goroutine. Per contract the caller holds b.mu;
	// the method blocks on the full channel. With the fix it releases b.mu during
	// that blocked send.
	go func() {
		b.mu.Lock()
		b.emitDesiredLocked(rt, "test")
		// returns with b.mu re-acquired
	}()

	// While emit is parked on the full channel, b.mu must be takable from here.
	muFree := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.mu.Lock()
		close(muFree)
		b.mu.Unlock()
	}()

	select {
	case <-muFree:
		// LOT-75 fix confirmed: b.mu released during blocked send.
	case <-time.After(2 * time.Second):
		t.Fatal("LOT-75 regression: b.mu held while DesiredState send is blocked")
	}
	wg.Wait()
}

// drainDesired pulls up to max events from the bus within the timeout.
func drainDesired(t *testing.T, bus *events.Bus, max int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for i := 0; i < max; i++ {
		select {
		case <-bus.DesiredState:
		case <-deadline:
			return
		}
	}
}
