// Package probe adds a rung-aware prober to the ones the daemon already uses
// (socks-through-the-box, box-direct, and the simulated one).
//
// It exists because of a defect observed live: a probe for a rung the selector is
// NOT currently on cannot be measured by sending traffic through that selector —
// the traffic follows whatever rung IS active, so the inactive rung is credited
// with the active rung's health. A dead VPN node therefore reported healthy 26ms
// replies while the brain sat on `direct`, which tripped a silent-recovery back
// onto the dead node, and the client flapped.
//
// The fix is to measure an INACTIVE vpn-class rung with the Clash delay test for
// that pool, which tests the pool itself and leaves the selector untouched.
package probe

import (
	"context"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// DefaultTestURL is the delay-test target for inactive VPN rungs. It only has to
// prove the pool can carry traffic; the service's own probe target is still used
// whenever the rung is the active one.
const DefaultTestURL = "https://www.gstatic.com/generate_204"

// Rung wraps a base prober and redirects probes of INACTIVE vpn-class rungs to a
// Clash delay test of that rung's pool.
type Rung struct {
	base    dataplane.Prober
	clash   *dataplane.ClashClient
	reg     *registry.Registry
	testURL string
	timeout time.Duration
}

// New wraps base. clash/reg must be non-nil; otherwise every probe delegates.
func New(base dataplane.Prober, clash *dataplane.ClashClient, reg *registry.Registry, testURL string, timeout time.Duration) *Rung {
	if testURL == "" {
		testURL = DefaultTestURL
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &Rung{base: base, clash: clash, reg: reg, testURL: testURL, timeout: timeout}
}

// Probe measures the rung at position for service.
func (r *Rung) Probe(ctx context.Context, service string, position int) events.ProductionVerdict {
	pool, ok := r.inactiveVPNPool(ctx, service, position)
	if !ok {
		// Either not a vpn rung, or it IS the active one — then routing a probe
		// through the selector genuinely measures it, and does so against the
		// service's own target, which is richer than a generic delay test.
		return r.base.Probe(ctx, service, position)
	}
	delay, err := r.clash.NodeDelay(ctx, pool, r.testURL, r.timeout)
	if err != nil {
		return events.ProductionVerdict{Service: service, Position: position, OK: false, Err: err.Error()}
	}
	return events.ProductionVerdict{Service: service, Position: position, OK: true, RTTms: delay}
}

// inactiveVPNPool returns the pool tag of the vpn-class rung at position when
// that rung is NOT the one the service's selector currently points at.
func (r *Rung) inactiveVPNPool(ctx context.Context, service string, position int) (string, bool) {
	if r.clash == nil || r.reg == nil {
		return "", false
	}
	svc, found := r.reg.Services[service]
	if !found {
		return "", false
	}
	pool := ""
	for _, step := range svc.Chain {
		if step.Position == position {
			if step.StrategyClass != strategy.ClassVPN || step.StrategyID == "" {
				return "", false
			}
			pool = step.StrategyID
			break
		}
	}
	if pool == "" {
		return "", false
	}
	info, err := r.clash.Proxy(ctx, registry.SelectorTag(service))
	if err != nil {
		// Cannot tell which rung is live; fall back to the ordinary path probe
		// rather than inventing a verdict.
		return "", false
	}
	if info.Now == pool {
		return "", false // this rung IS active — probe it for real
	}
	return pool, true
}
