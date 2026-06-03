package dataplane

import (
	"context"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
)

// FuncProber decides probe outcomes from a caller-supplied function of
// (service, position, elapsed). It exists to drive the control loop on a
// timeline without real hardware — the M0 walking-skeleton demo and tests use
// it to script fail->escalate and recover->silent-success sequences.
//
// On real hardware the HTTPProber handles active liveness; true silent
// recovery probing of an inactive strategy needs the qnum-999 sandbox (the
// Tester component), which is deferred past M0.
type FuncProber struct {
	Fn    func(service string, position int, elapsed time.Duration) (ok bool, rttMs int)
	start time.Time
}

// NewFuncProber starts the elapsed-time clock now.
func NewFuncProber(fn func(service string, position int, elapsed time.Duration) (bool, int)) *FuncProber {
	return &FuncProber{Fn: fn, start: time.Now()}
}

func (f *FuncProber) Probe(_ context.Context, service string, position int) events.ProductionVerdict {
	ok, rtt := f.Fn(service, position, time.Since(f.start))
	v := events.ProductionVerdict{Service: service, Position: position, OK: ok, RTTms: rtt}
	if !ok {
		v.Err = "scripted failure"
	}
	return v
}
