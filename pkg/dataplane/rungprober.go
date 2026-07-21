package dataplane

import (
	"context"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// DefaultRungTestURL is the delay-test target used for an inactive VPN rung. It
// only has to prove the pool can carry traffic; a rung that IS active is still
// probed against the service's own target, which is a richer signal.
const DefaultRungTestURL = "https://www.gstatic.com/generate_204"

// RungProber wraps another Prober and redirects probes of INACTIVE vpn-class
// rungs to a Clash delay test of that rung's pool.
//
// It exists because a probe of a rung the selector is not currently on cannot be
// measured by sending traffic through that selector: the traffic follows whatever
// rung IS active, so the inactive rung is credited with the active rung's health.
// Observed live — a dead VPN pool kept returning healthy replies after the brain
// had escalated to direct, which tripped a false silent-recovery back onto it.
// The Clash delay test measures the pool itself and leaves the selector alone.
type RungProber struct {
	base    Prober
	clash   *ClashClient
	reg     *registry.Registry
	testURL string
	timeout time.Duration
}

// NewRungProber wraps base. A nil clash or reg makes every probe delegate, so
// wiring it is always safe.
//
// timeout bounds how long sing-box may spend on the delay test. It is clamped
// well under the Clash client's own HTTP timeout: asking for a test at least as
// long as that budget makes the client abandon the request before sing-box can
// answer, turning a healthy-but-slow pool into a spurious failure.
func NewRungProber(base Prober, clash *ClashClient, reg *registry.Registry, testURL string, timeout time.Duration) *RungProber {
	if testURL == "" {
		testURL = DefaultRungTestURL
	}
	budget := 3 * time.Second
	if clash != nil && clash.Client != nil && clash.Client.Timeout > 0 {
		budget = clash.Client.Timeout
	}
	if max := budget * 2 / 3; timeout <= 0 || timeout > max {
		timeout = max
	}
	return &RungProber{base: base, clash: clash, reg: reg, testURL: testURL, timeout: timeout}
}

// Probe measures the rung at position for service.
func (r *RungProber) Probe(ctx context.Context, service string, position int) events.ProductionVerdict {
	pool, ok := r.inactiveVPNPool(ctx, service, position)
	if !ok {
		return r.base.Probe(ctx, service, position)
	}
	delay, err := r.clash.NodeDelay(ctx, pool, r.testURL, r.timeout)
	if err != nil {
		return events.ProductionVerdict{Service: service, Position: position, OK: false, Err: err.Error()}
	}
	return events.ProductionVerdict{Service: service, Position: position, OK: true, RTTms: delay}
}

// inactiveVPNPool returns the pool tag of the vpn-class rung at position, but
// only when that rung is NOT the one the service's selector currently points at.
func (r *RungProber) inactiveVPNPool(ctx context.Context, service string, position int) (string, bool) {
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
		// Cannot tell which rung is live — fall back to the ordinary path probe
		// rather than inventing a verdict.
		return "", false
	}
	if info.Now == pool {
		return "", false // this rung IS active: probe it for real
	}
	return pool, true
}
