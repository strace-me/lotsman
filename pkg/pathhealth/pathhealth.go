// Package pathhealth is the DETECT half of escalation-model v2 (LOT-33-design,
// E-1): instead of walking a service's fallback chain one rung at a time
// (fail → wait → fail → wait → …), it probes EVERY chain step in PARALLEL,
// out-of-band, and reports which tiers actually work right now — so the ACT side
// (Brain, later slices) can jump straight to the best working tier.
//
// "Out-of-band" is the crux: it must test each step's PATH without flipping the
// live selector. The mapping:
//
//   - zapret/direct steps → a BOX-DIRECT probe of the service target (egress
//     traverses nfqws on the direct path; independent of the selector).
//   - vpn/emergency steps → clash NodeDelay against that step's pool/outbound,
//     which the Clash API measures without touching the active selector.
//
// E-1 is PROPOSE-ONLY: Scan logs the health vector and the best working tier vs
// the current position. It changes nothing — it is pure observability that also
// establishes the fan-out the later (acting) slices feed into Brain.
package pathhealth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// Positioner reports a service's current chain position. Brain implements it.
type Positioner interface {
	Position(service string) int
}

// NodeDelayer measures an outbound's HTTP delay out-of-band (Clash API). The
// ClashClient implements it; tests fake it.
type NodeDelayer interface {
	NodeDelay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error)
}

// PosHealth is one chain step's out-of-band health.
type PosHealth struct {
	Position int
	State    string // chain step state (PREFERRED/VPN/…)
	Class    string // zapret/vpn/direct/emergency
	OK       bool
	RTTms    int
	Err      string
}

// PathHealth is the health vector across a service's whole chain, ordered by
// position (index 0 = best tier).
type PathHealth struct {
	Service string
	Current int
	Steps   []PosHealth
}

// BestWorking returns the lowest position (best tier) that probed healthy, or
// -1 if every step is down.
func (p PathHealth) BestWorking() int {
	for i := range p.Steps {
		if p.Steps[i].OK {
			return i
		}
	}
	return -1
}

// Detector fans out per-step probes for every service and logs the result.
// All probes are out-of-band; nothing is applied (E-1 propose-only).
type Detector struct {
	Reg         *registry.Registry
	Pos         Positioner
	Direct      dataplane.Prober // BOX-DIRECT prober (no socks proxy) for zapret/direct steps
	Nodes       NodeDelayer      // for vpn/emergency steps
	TestURL     string           // generic connectivity URL for the vpn-tier probe of non-HTTP services
	Timeout     time.Duration
	MaxParallel int // cap on concurrent step probes per service (<=0 => 4)
	Log         *slog.Logger
}

// Scan probes every probeable service's chain and logs the best working tier vs
// the current position. Returns nil (it is observe-only); a probe failure is a
// data point, not an error. Shaped as periodic.Task.Fn.
func (d *Detector) Scan(ctx context.Context) error {
	for name, svc := range d.Reg.Services {
		if !probeable(svc) {
			continue
		}
		d.logPath(d.probeService(ctx, name, svc))
	}
	return nil
}

// probeable skips services we can't meaningfully fan out over: no probe target
// (LOCKED direct-only like ru-direct), nailed (Static), or a trivial 1-step chain.
func probeable(svc registry.Service) bool {
	return svc.ProbeTarget != "" && !svc.Static && len(svc.Chain) > 1
}

func (d *Detector) probeService(ctx context.Context, service string, svc registry.Service) PathHealth {
	steps := make([]PosHealth, len(svc.Chain))
	limit := d.MaxParallel
	if limit <= 0 {
		limit = 4
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, st := range svc.Chain {
		wg.Add(1)
		go func(i int, st registry.ChainStep) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			steps[i] = d.probeStep(ctx, svc, i, st) // each goroutine writes its own index — no race
		}(i, st)
	}
	wg.Wait()
	return PathHealth{Service: service, Current: d.Pos.Position(service), Steps: steps}
}

func (d *Detector) probeStep(ctx context.Context, svc registry.Service, pos int, st registry.ChainStep) PosHealth {
	h := PosHealth{Position: pos, State: st.State, Class: st.StrategyClass}
	switch st.StrategyClass {
	case strategy.ClassVPN, strategy.ClassEmergency:
		if st.StrategyID == "" {
			h.Err = "no pool id for vpn/emergency step"
			return h
		}
		ms, err := d.Nodes.NodeDelay(ctx, st.StrategyID, d.vpnTestURL(svc), d.timeout())
		if err != nil {
			h.Err = err.Error()
			return h
		}
		h.OK, h.RTTms = true, ms
	case strategy.ClassZapret, strategy.ClassByeDPI, strategy.ClassDirect:
		v := d.Direct.Probe(ctx, svc.Name, pos) // box-direct: egress→nfqws on the direct path
		h.OK, h.RTTms, h.Err = v.OK, v.RTTms, v.Err
	default:
		h.Err = "unprobeable class " + st.StrategyClass
	}
	return h
}

// vpnTestURL is the URL NodeDelay GETs through the pool. HTTP services already
// carry a URL target; TCP/STUN services (voice) carry host:port, so fall back to
// a generic connectivity URL (we are testing the TIER is up, not the exact app).
func (d *Detector) vpnTestURL(svc registry.Service) string {
	if svc.ProbeType == "" || svc.ProbeType == dataplane.ProbeHTTP {
		return svc.ProbeTarget
	}
	return d.TestURL
}

func (d *Detector) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 4 * time.Second
}

func (d *Detector) logPath(p PathHealth) {
	best := p.BestWorking()
	var parts []string
	for _, s := range p.Steps {
		mark := "✗"
		if s.OK {
			mark = fmt.Sprintf("✓%dms", s.RTTms)
		}
		parts = append(parts, fmt.Sprintf("%d:%s:%s", s.Position, s.Class, mark))
	}
	steps := strings.Join(parts, " ")
	switch {
	case best == -1:
		d.Log.Warn("path-health: ALL chain steps down", "service", p.Service, "current", p.Current, "steps", steps)
	case best < p.Current:
		d.Log.Info("path-health: PROPOSE faster tier available (would jump current→best)",
			"service", p.Service, "current", p.Current, "best", best, "steps", steps)
	default:
		d.Log.Info("path-health: current tier is best working", "service", p.Service, "current", p.Current, "best", best, "steps", steps)
	}
}
