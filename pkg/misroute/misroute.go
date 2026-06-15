// Package misroute is the runtime-coherence detector (LOT-16, phase 1 of the
// self-heal epic). It turns the passive-observation eye's snapshot
// (pkg/observe) into per-service misroute verdicts: a service is "misrouted"
// when its traffic is supposed to ride its VPN selector but a significant
// share is leaking to direct (the LOT-2 YouTube-QUIC failure mode), or when
// its matched UDP/QUIC flows are stalling (dead).
//
// Phase 1 is DETECT only: Detect is a pure function with no I/O. It emits a
// verdict per service; callers log it and publish a metric. There is NO
// remediation and NO Brain escalation here — that is LOT-18. The Verdict
// struct is the seam a future remediator will consume.
package misroute

import (
	"fmt"

	"github.com/strace-me/lotsman/pkg/observe"
)

// Verdict kinds.
const (
	KindNone    = ""        // not misrouted
	KindLeak    = "leak"    // flows leaking to direct above threshold
	KindDead    = "dead"    // matched UDP/QUIC flows stalled above threshold
	KindStalled = "stalled" // flows frozen mid-stream (TSPU IP-throttle); only a foreign egress escapes it (LOT-43)
)

// Config holds the detection thresholds. Zero value is unusable; use
// DefaultConfig and override fields as needed.
type Config struct {
	// LeakRatioThreshold: a service leaks when LeakRatio exceeds this.
	LeakRatioThreshold float64
	// MinFlows: minimum matched flows before a leak verdict is trusted
	// (avoids noise on tiny samples).
	MinFlows int

	// DeadFlowRatioThreshold: a service is dead when DeadFlowRatio exceeds this.
	DeadFlowRatioThreshold float64
	// MinUDPFlows: minimum matched UDP/QUIC flows before a dead verdict is
	// trusted.
	MinUDPFlows int

	// StalledRatioThreshold: a service is throttle-stalled when StalledRatio
	// exceeds this. MinStallFlows: minimum matched flows before a stalled verdict
	// is trusted (the throttle shows as several frozen flows + churn, LOT-43).
	StalledRatioThreshold float64
	MinStallFlows         int
}

// DefaultConfig returns sane, service-agnostic defaults.
//
// LeakRatioThreshold 0.3: in the LOT-2 incident ~⅔ of googlevideo QUIC leaked
// to direct, so 0.3 catches it with margin while staying above the noise of
// the odd un-sniffed flow. MinFlows 4: a service needs a handful of live flows
// before a ratio is meaningful (1/2 leaking is not yet a pattern).
//
// DeadFlowRatioThreshold 0.5: a healthy service occasionally has a freshly
// opened or genuinely idle UDP flow with 0 download; requiring a majority
// stalled avoids flagging those. MinUDPFlows 4: same small-sample guard for
// the UDP denominator.
func DefaultConfig() Config {
	return Config{
		LeakRatioThreshold:     0.3,
		MinFlows:               4,
		DeadFlowRatioThreshold: 0.5,
		MinUDPFlows:            4,
		StalledRatioThreshold:  0.5,
		MinStallFlows:          2,
	}
}

// Verdict is the per-service detection result. Misrouted == false means the
// service is healthy (Kind is KindNone). When Misrouted, Kind names the
// dominant failure and Reason is a human-readable explanation. The struct is
// the contract a future remediator (LOT-18) consumes.
type Verdict struct {
	Service       string
	Misrouted     bool
	Kind          string // KindLeak | KindDead | KindStalled | KindNone
	LeakRatio     float64
	DeadFlowRatio float64
	StalledRatio  float64
	Reason        string
}

// Detect evaluates every service in the snapshot against the config and returns
// one verdict per service. It is pure: no I/O, no mutation of the snapshot.
//
// Leak is checked before dead: a service leaking to direct is the more
// actionable signal (the traffic never reached the tunnel at all), so it wins
// when both fire. Verdicts are returned in the snapshot's map order; callers
// that need stable output should sort by Service.
func Detect(snap observe.Snapshot, cfg Config) []Verdict {
	out := make([]Verdict, 0, len(snap.Services))
	for _, sm := range snap.Services {
		out = append(out, detectOne(sm, cfg))
	}
	return out
}

func detectOne(sm observe.ServiceMetrics, cfg Config) Verdict {
	v := Verdict{
		Service:       sm.Service,
		Kind:          KindNone,
		LeakRatio:     sm.LeakRatio,
		DeadFlowRatio: sm.DeadFlowRatio,
		StalledRatio:  sm.StalledRatio,
	}

	leaking := sm.Flows >= cfg.MinFlows && sm.LeakRatio > cfg.LeakRatioThreshold
	// Stalled = a majority of the service's flows frozen mid-stream (the TSPU
	// IP-throttle). Only a foreign egress escapes an IP-keyed throttle (desync /
	// reject-quic can't), so this verdict must drive a chain ESCALATION, not a
	// route toggle — handled downstream (LOT-43).
	stalled := sm.Flows >= cfg.MinStallFlows && sm.StalledRatio > cfg.StalledRatioThreshold
	// A "dead" verdict drives the reject-quic remedy (force udp/443 → TCP), so it
	// must be a real QUIC stall: require at least one dead flow on udp/443. A
	// dead-ratio made up entirely of one-way VOICE/RTC flows (DeadQUICFlows==0) is
	// self-healing — the client renegotiates — and reject-quic cannot fix it; firing
	// there just flaps an ineffective remediation (LOT-35). WedgedOneWayRTC surfaces
	// that case separately as a log/metric.
	dead := sm.UDPFlows >= cfg.MinUDPFlows && sm.DeadFlowRatio > cfg.DeadFlowRatioThreshold && sm.DeadQUICFlows > 0

	switch {
	case leaking:
		v.Misrouted = true
		v.Kind = KindLeak
		v.Reason = reasonLeak(sm, cfg)
	case stalled:
		v.Misrouted = true
		v.Kind = KindStalled
		v.Reason = reasonStalled(sm, cfg)
	case dead:
		v.Misrouted = true
		v.Kind = KindDead
		v.Reason = reasonDead(sm, cfg)
	}
	return v
}

func reasonStalled(sm observe.ServiceMetrics, cfg Config) string {
	return fmt.Sprintf("stalled: %d/%d flows frozen mid-stream (ratio %.2f > %.2f) — TSPU IP-throttle; escalate to a foreign egress",
		sm.FrozenFlows, sm.Flows, sm.StalledRatio, cfg.StalledRatioThreshold)
}

func reasonLeak(sm observe.ServiceMetrics, cfg Config) string {
	return fmt.Sprintf("leak: %d/%d flows leaked to direct (ratio %.2f > %.2f); service should ride its selector",
		sm.LeakFlows, sm.Flows, sm.LeakRatio, cfg.LeakRatioThreshold)
}

func reasonDead(sm observe.ServiceMetrics, cfg Config) string {
	return fmt.Sprintf("dead: %d/%d UDP/QUIC flows stalled at ~0 download (ratio %.2f > %.2f)",
		sm.DeadUDPFlows, sm.UDPFlows, sm.DeadFlowRatio, cfg.DeadFlowRatioThreshold)
}
