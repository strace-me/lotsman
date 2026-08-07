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
	// direct probes a rung by dialing straight out, bypassing the selector. It is
	// set (via SetDirectProber) only where a genuine direct path exists — the
	// client's proxy mode. When nil, non-VPN rungs fall through to base, which is
	// the router's current behaviour, so the router is byte-for-byte unaffected.
	direct Prober
}

// SetDirectProber wires the prober used for an INACTIVE non-VPN (zapret/direct)
// rung: one that dials direct instead of through the selector. Without it, such a
// rung is probed through whatever the selector currently points at — a VPN node —
// and credited with that node's health, the same false-recovery the VPN case was
// built to avoid (LOT-44). Only meaningful where "direct" is really direct (proxy
// mode); in tun mode the host's own tun would capture it, so it is left unset.
func (r *RungProber) SetDirectProber(p Prober) { r.direct = p }

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
	if pool, ok := r.inactiveVPNPool(ctx, service, position); ok {
		delay, err := r.clash.NodeDelay(ctx, pool, r.testURL, r.timeout)
		if err != nil {
			return events.ProductionVerdict{Service: service, Position: position, OK: false, Err: err.Error()}
		}
		return events.ProductionVerdict{Service: service, Position: position, OK: true, RTTms: delay}
	}
	// An inactive zapret/direct rung routes DIRECT; while the service sits on a VPN
	// node the ordinary probe would follow the selector and measure that node, not
	// the direct path this rung uses. Probe direct instead so the verdict is about
	// the rung being tested (LOT-44).
	if r.inactiveDirectRouted(ctx, service, position) {
		if r.direct != nil {
			return r.direct.Probe(ctx, service, position)
		}
		// No direct prober — tun mode. Falling through to base here was the whole
		// point of LOT-44 and it was left in place for exactly one configuration:
		// the packet follows the service's route rule to the VPN node, and the
		// desync rung is credited with the tunnel's health. Measured on the
		// ThinkPad while YouTube worked only through the VPN: silent probes of
		// positions 0 and 1 accumulated successes toward a recovery that would put
		// the service back on a rung nobody had measured, and every one of those
		// successes was also recorded in the knowledge base against the desync
		// recipe. Refuse instead — an unmeasured rung earns no recovery and
		// teaches the KB nothing.
		return events.ProductionVerdict{
			Service: service, Position: position, Unmeasured: true,
			Err: "cannot probe an inactive direct rung in tun mode: the tun would route this through the active VPN node and credit the desync with its health",
		}
	}
	return r.base.Probe(ctx, service, position)
}

// inactiveDirectRouted reports whether position is a non-VPN (direct-routed) rung
// the service is NOT currently on — i.e. the selector points at a VPN node, so the
// ordinary path probe would mis-measure this rung.
func (r *RungProber) inactiveDirectRouted(ctx context.Context, service string, position int) bool {
	if r.clash == nil || r.reg == nil {
		return false
	}
	svc, ok := r.reg.Services[service]
	if !ok {
		return false
	}
	found := false
	var step registry.ChainStep
	for _, s := range svc.Chain {
		if s.Position == position {
			step, found = s, true
			break
		}
	}
	if !found || step.StrategyClass == strategy.ClassVPN {
		return false // the VPN case is handled by inactiveVPNPool
	}
	info, err := r.clash.Proxy(ctx, registry.SelectorTag(service))
	if err != nil {
		return false // cannot tell which rung is live — do not divert the probe
	}
	// "direct" (or unset) means the service already routes direct, so the ordinary
	// probe measures the right path. Only a VPN node makes this rung inactive.
	return info.Now != "direct" && info.Now != ""
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
