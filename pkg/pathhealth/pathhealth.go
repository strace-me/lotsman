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
// All probes are out-of-band; nothing is applied (E-1 propose-only). Each Scan
// also caches the latest health vector per service so Brain can consult it via
// NextWorking (the E-2 PathOracle) to jump straight to the best working tier.
type Detector struct {
	Reg           *registry.Registry
	Pos           Positioner
	Direct        dataplane.Prober // BOX-DIRECT prober (no socks proxy) for zapret/direct steps
	Nodes         NodeDelayer      // for vpn/emergency steps
	TestURL       string           // generic connectivity URL for the vpn-tier probe of non-HTTP services
	Timeout       time.Duration    // probe timeout for direct/zapret steps (<=0 => 4s)
	VPNTimeout    time.Duration    // probe timeout for vpn/emergency steps — longer for cold hysteria QUIC handshake (<=0 => 8s)
	VPNAttempts   int              // warm-up: retry a vpn/emergency probe up to N times, healthy if ANY succeeds (<=0 => 2)
	RecoverStreak int              // E-4: a lower tier must be healthy this many consecutive scans before recovery (return slow) (<=0 => 3)
	MaxParallel   int              // cap on concurrent step probes per service (<=0 => 4)
	Log           *slog.Logger

	mu     sync.Mutex
	latest map[string]PathHealth  // service -> most recent scan (for NextWorking)
	up     map[string]map[int]int // service -> position -> consecutive healthy scans (for RecoverTarget)
}

// Scan probes every probeable service's chain and logs the best working tier vs
// the current position. Returns nil (it is observe-only); a probe failure is a
// data point, not an error. Shaped as periodic.Task.Fn.
func (d *Detector) Scan(ctx context.Context) error {
	for name, svc := range d.Reg.Services {
		if !probeable(svc) {
			continue
		}
		ph := d.probeService(ctx, name, svc)
		d.store(ph)
		d.logPath(ph)
	}
	return nil
}

// store caches the latest scan for a service (for NextWorking). Held briefly,
// never around a probe or a Brain call, so it cannot deadlock with Brain's lock.
func (d *Detector) store(ph PathHealth) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.latest == nil {
		d.latest = map[string]PathHealth{}
	}
	d.latest[ph.Service] = ph
	// Per-position consecutive-healthy-scan streak, for recovery hysteresis (E-4).
	if d.up == nil {
		d.up = map[string]map[int]int{}
	}
	s := d.up[ph.Service]
	if s == nil {
		s = map[int]int{}
		d.up[ph.Service] = s
	}
	for _, st := range ph.Steps {
		if st.OK {
			s[st.Position]++
		} else {
			s[st.Position] = 0
		}
	}
}

// NextWorking is the E-2 PathOracle: the lowest position STRICTLY ABOVE `above`
// that the most recent scan saw healthy, or -1 if none/unknown (no scan yet).
// Brain uses it to escalate straight to the best working tier instead of walking
// the chain one rung at a time. Safe for concurrent use.
func (d *Detector) NextWorking(service string, above int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	ph, ok := d.latest[service]
	if !ok {
		return -1
	}
	for i := range ph.Steps {
		if ph.Steps[i].Position > above && ph.Steps[i].OK {
			return ph.Steps[i].Position
		}
	}
	return -1
}

// RecoverTarget is the E-4 recovery oracle: the lowest position STRICTLY BELOW
// `below` that has been healthy for RecoverStreak consecutive scans (return
// slow — asymmetric vs NextWorking's leave-fast), or -1. Lets Brain recover
// straight to the best available lower tier instead of one rung at a time.
func (d *Detector) RecoverTarget(service string, below int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	ph, ok := d.latest[service]
	if !ok {
		return -1
	}
	need := d.RecoverStreak
	if need <= 0 {
		need = 3
	}
	s := d.up[service]
	for i := range ph.Steps {
		p := ph.Steps[i].Position
		if p >= below {
			break // ordered ascending; only positions below current matter
		}
		if ph.Steps[i].OK && s[p] >= need {
			return p
		}
	}
	return -1
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
		// Warm-up tolerance: hysteria (QUIC) does not come up instantly, so retry
		// up to N times with a generous timeout — healthy if ANY attempt responds.
		// This stops a cold pool from being a false "down" (and E-2 is fail-safe
		// either way: an unconfirmed tier just isn't a jump target).
		var ms int
		var err error
		for attempt := 0; attempt < d.vpnAttempts(); attempt++ {
			ms, err = d.Nodes.NodeDelay(ctx, st.StrategyID, d.vpnTestURL(svc), d.vpnTimeout())
			if err == nil {
				h.OK, h.RTTms = true, ms
				return h
			}
			if ctx.Err() != nil {
				break
			}
		}
		h.Err = err.Error()
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

func (d *Detector) vpnTimeout() time.Duration {
	if d.VPNTimeout > 0 {
		return d.VPNTimeout
	}
	return 8 * time.Second
}

func (d *Detector) vpnAttempts() int {
	if d.VPNAttempts > 0 {
		return d.VPNAttempts
	}
	return 2
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
	case best > p.Current:
		// The rung the rule is ON did not answer, and the nearest one that did is
		// further down the chain. This used to fall into the branch below and print
		// "current tier is best working" over a tier marked ✗ — an assertion made by
		// code that had just observed the opposite. Seen on the office network with
		// youtube at current=2 while steps read 0:direct:✗ 1:zapret:✗ 2:zapret:✗
		// 3:vpn:✓. Escalation itself was right (it asks NextWorking); only the
		// sentence was wrong, which is the harder kind to notice.
		d.Log.Warn("path-health: the current tier is DOWN, the nearest working one is below it",
			"service", p.Service, "current", p.Current, "best", best, "steps", steps)
	default:
		d.Log.Info("path-health: current tier is best working", "service", p.Service, "current", p.Current, "best", best, "steps", steps)
	}
}
