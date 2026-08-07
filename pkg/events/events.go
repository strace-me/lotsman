// Package events defines the cross-component message contract and an in-proc bus.
//
// The same message types are used whether components run in one process
// (channels, this file) or across processes (NATS/REST in a later phase). The
// split is mechanical: replace the channel bus with a network transport, the
// message shapes do not change.
//
// M0 wires three of the loops described in the architecture:
//
//	Feedback: prober ──ProductionVerdict──▶ Brain (+ KB)
//	Control:  Brain   ──DesiredStateChanged──▶ Applier
//	Reconcile: Applier ──ActualStateObserved──▶ Brain
//
// Discovery (Tester→KB) and async recommendation (Brain↔KB request/reply) are
// not on the bus in M0: there is no Tester yet, and Brain consults the KB
// synchronously through an interface. Those become bus messages when the
// daemon is split.
package events

// ProductionVerdict reports the result of a probe against a service's traffic
// in production. Position identifies which chain step was probed: the active
// position (liveness of the current strategy) or a lower position (silent
// recovery probe for a preferred strategy).
type ProductionVerdict struct {
	Service  string
	Position int
	OK       bool
	RTTms    int
	Err      string
	// Unmeasured means the probe COULD NOT reach the rung it was asked about, so
	// neither OK nor its opposite is a claim about that rung. A reader must count
	// it as nothing at all: not a success, not a failure, not an EWMA sample.
	//
	// It exists because the alternative is a lie in one direction or the other. In
	// tun mode a silent probe of an inactive desync rung follows the service's
	// route rule to whatever the selector points at — the VPN — so `OK` credited
	// the desync with the tunnel's health and `!OK` would blame it for a path it
	// never touched. Both poison the knowledge base; only silence is true.
	Unmeasured bool
}

// DesiredStateChanged is Brain's decision about what should be active for a
// service. Applier reconciles production toward it. Emitting the same desired
// state twice must be safe (Applier apply is idempotent).
type DesiredStateChanged struct {
	Service       string
	Position      int
	State         string
	StrategyClass string
	StrategyID    string
}

// ActualStateObserved is Applier's report of what is actually active after a
// reconcile. Brain compares it to desired to detect drift (crash recovery,
// failed apply, external mutation).
type ActualStateObserved struct {
	Service  string
	Position int
	State    string
}

// Bus is the in-proc event transport: three typed channels, one per loop.
// Typed channels keep the contract compile-checked (a stated reason for Go).
// A cross-proc transport implements the same message types over the network.
type Bus struct {
	Verdicts     chan ProductionVerdict
	DesiredState chan DesiredStateChanged
	ActualState  chan ActualStateObserved
}

// NewBus returns a Bus with small buffered channels. Buffering decouples
// producers from consumers without unbounded growth; M0 traffic is tiny.
func NewBus() *Bus {
	const buf = 16
	return &Bus{
		Verdicts:     make(chan ProductionVerdict, buf),
		DesiredState: make(chan DesiredStateChanged, buf),
		ActualState:  make(chan ActualStateObserved, buf),
	}
}
