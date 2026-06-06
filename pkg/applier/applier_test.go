package applier

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/executor"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeExecutor is a StrategyExecutor that reports Enable calls on a channel and
// returns a scripted error. It stands in for the real ScriptSwitcher/VPN/Direct
// executors. Calls are surfaced over a channel (not a slice) so the test
// goroutine never races the Run goroutine that invokes Enable.
type fakeExecutor struct {
	class string
	err   error
	calls chan string // "service|strategyID" per Enable
}

func newFakeExecutor(class string, err error) *fakeExecutor {
	return &fakeExecutor{class: class, err: err, calls: make(chan string, 8)}
}

func (f *fakeExecutor) Class() string { return f.class }

func (f *fakeExecutor) Enable(_ context.Context, service, strategyID string) error {
	f.calls <- service + "|" + strategyID
	return f.err
}

// runApplier starts a.Run in the background and returns a cancel func.
func runApplier(a *Applier) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	return cancel, done
}

// TestRoutesToMatchingClassAndReportsActual covers (a) routing by class and
// (b) the success feedback contract: a successful Enable publishes
// ActualStateObserved mirroring the desired state.
func TestRoutesToMatchingClassAndReportsActual(t *testing.T) {
	bus := events.NewBus()
	zap := newFakeExecutor("zapret", nil)
	vpn := newFakeExecutor("vpn", nil)
	a := New(bus, []executor.StrategyExecutor{zap, vpn}, discardLog())

	cancel, done := runApplier(a)
	defer func() { cancel(); <-done }()

	bus.DesiredState <- events.DesiredStateChanged{
		Service: "youtube", Position: 2, State: "VPN",
		StrategyClass: "vpn", StrategyID: "vpn_url_test",
	}

	// Routed to the vpn executor with the desired strategy id.
	select {
	case got := <-vpn.calls:
		if got != "youtube|vpn_url_test" {
			t.Errorf("vpn Enable arg = %q, want youtube|vpn_url_test", got)
		}
	case <-time.After(time.Second):
		t.Fatal("vpn executor was not called")
	}

	select {
	case act := <-bus.ActualState:
		if act.Service != "youtube" || act.Position != 2 || act.State != "VPN" {
			t.Fatalf("actual = %+v, want service=youtube pos=2 state=VPN", act)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ActualStateObserved")
	}

	// The non-matching executor must not have been called.
	select {
	case got := <-zap.calls:
		t.Errorf("zapret executor was called: %q", got)
	default:
	}
}

// TestEnableErrorSuppressesActual is the idempotency/feedback contract: when
// Enable fails, the applier must NOT report actual=desired, so Brain keeps
// seeing stale actual and re-emits desired (the reconcile retry).
func TestEnableErrorSuppressesActual(t *testing.T) {
	bus := events.NewBus()
	zap := newFakeExecutor("zapret", errors.New("nfqws restart failed"))
	a := New(bus, []executor.StrategyExecutor{zap}, discardLog())

	cancel, done := runApplier(a)
	defer func() { cancel(); <-done }()

	bus.DesiredState <- events.DesiredStateChanged{
		Service: "youtube", Position: 0, State: "PREFERRED",
		StrategyClass: "zapret", StrategyID: "alt12",
	}

	// Enable must still have been attempted...
	select {
	case <-zap.calls:
	case <-time.After(time.Second):
		t.Fatal("Enable was not called on a failing executor")
	}
	// ...but no ActualStateObserved may be published.
	select {
	case act := <-bus.ActualState:
		t.Fatalf("actual published after Enable error: %+v", act)
	case <-time.After(150 * time.Millisecond):
		// expected: nothing
	}
}

// TestApplierKeepsDrainingWhenActualStateFull is the LOT-37 deadlock regression
// guard: a saturated ActualState channel must NOT stop the read loop from
// draining DesiredState. Pre-fix the inline ActualState send blocked the read
// loop, which (with Brain blocked sending DesiredState) deadlocked the control
// loop. The forwarder goroutine now absorbs ActualState backpressure.
func TestApplierKeepsDrainingWhenActualStateFull(t *testing.T) {
	bus := events.NewBus()
	vpn := newFakeExecutor("vpn", nil)
	a := New(bus, []executor.StrategyExecutor{vpn}, discardLog())

	// Saturate ActualState so the forwarder's send blocks (nobody is draining it).
	for {
		select {
		case bus.ActualState <- events.ActualStateObserved{Service: "filler"}:
			continue
		default:
		}
		break
	}

	cancel, done := runApplier(a)
	defer func() { cancel(); <-done }()

	// Both desired events must be applied despite ActualState being full.
	for i := 0; i < 2; i++ {
		bus.DesiredState <- events.DesiredStateChanged{
			Service: "youtube", Position: i, State: "VPN", StrategyClass: "vpn", StrategyID: "p",
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-vpn.calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("Applier stalled under ActualState backpressure after %d/2 enables", i)
		}
	}
}

// TestUnknownStrategyClassNoPanic: a desired event for a class with no
// registered executor is logged and dropped, never panics, never reports.
func TestUnknownStrategyClassNoPanic(t *testing.T) {
	bus := events.NewBus()
	a := New(bus, nil, discardLog()) // no executors registered

	cancel, done := runApplier(a)
	defer func() { cancel(); <-done }()

	bus.DesiredState <- events.DesiredStateChanged{
		Service: "youtube", Position: 0, State: "PREFERRED",
		StrategyClass: "ghost", StrategyID: "x",
	}

	select {
	case act := <-bus.ActualState:
		t.Fatalf("actual published for unknown class: %+v", act)
	case <-time.After(150 * time.Millisecond):
		// expected: dropped without panic or report
	}
}
