// Package observe is the passive-observation "eye" (LOT-15, phase 1 of the
// self-heal epic). It reads the live connection table from sing-box's Clash-API
// (GET /connections) and computes per-service real-traffic metrics in Go — no
// shell, no probing. It only OBSERVES: it emits metrics and a log summary; it
// never flips selectors, never escalates, never issues a verdict. Remediation
// and Brain integration are later phases.
//
// The signal it surfaces is misrouting: a flow that belongs to a service whose
// intended path is its sel-<svc> VPN selector, but that actually went out
// "direct", is a leak (the LOT-2 YouTube-QUIC-to-direct failure mode). It also
// flags dead UDP/QUIC flows (matched, ~0 download) which is what a stalled
// video looks like on the wire.
package observe

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/strace-me/lotsman/pkg/registry"
)

// directOutbound is the sing-box final outbound name for un-tunnelled traffic.
const directOutbound = "direct"

// deadDownloadBytes is the threshold at/below which a matched UDP/QUIC flow is
// "dead": the box uploaded handshake bytes but got ~nothing back (the LOT-2
// conntrack signature was packets=0 bytes=0). Kept at 0 — a strict stall — to
// avoid flagging brief or low-rate flows as dead.
const deadDownloadBytes int64 = 0

// quicPort is udp/443 — QUIC. A dead UDP flow on this port is a genuine QUIC
// stall (reject-quic fixes it); dead flows on other UDP ports are voice/RTC.
const quicPort = 443

// Conn is the subset of a sing-box /connections record the eye needs, decoupled
// from the dataplane HTTP types so observe depends only on registry. main.go
// maps dataplane.Connection into this shape.
//
// Chains is the outbound path: chains[0] is the final outbound ("direct" or a
// node tag). Upload/Download are cumulative bytes. Host is the sniffed SNI (may
// be empty when sniff failed — that flow is matched by DestIP instead).
type Conn struct {
	ID       string // stable per-connection id (for cross-pass stall detection, LOT-43)
	Chains   []string
	Upload   int64
	Download int64
	Host     string
	DestIP   string
	DestPort int    // destination port (0 if unknown); distinguishes QUIC (udp/443) from voice/RTC UDP
	Network  string // "tcp" | "udp"
	// Rule is sing-box's matched routing rule string, e.g.
	// "rule_set=geosite-youtube => route(sel-youtube)". It carries the ROUTE
	// TARGET (the selector sing-box chose), which attributes rule_set-routed flows
	// the host/IP matchers miss (LOT-20). Empty for direct/final flows.
	Rule string
}

func (c Conn) finalOutbound() string {
	if len(c.Chains) == 0 {
		return ""
	}
	return c.Chains[0]
}

// Source provides the live connection table. *dataplane.ClashClient is adapted
// to this in main.go; the interface keeps the eye testable without HTTP.
type Source interface {
	Connections(ctx context.Context) ([]Conn, error)
}

// ServiceMetrics is the observed per-service snapshot for one eye pass.
type ServiceMetrics struct {
	Service string
	// Flows is the number of live connections matched to this service.
	Flows int
	// LeakFlows is matched flows whose final outbound is "direct" while the
	// service is tunnel-intended (PREFERRED step is VPN, i.e. it should have
	// ridden its selector). Zapret/direct-preferred services route "direct" by
	// design, so their direct flows are not leaks (LOT-22).
	LeakFlows int
	// UDPFlows is matched UDP/QUIC connections (the dead-flow denominator).
	UDPFlows int
	// DeadUDPFlows is matched UDP/QUIC connections with ~0 download bytes.
	DeadUDPFlows int
	// DeadQUICFlows is the subset of DeadUDPFlows on udp/443 (QUIC). It gates the
	// reject-quic remediation: reject-quic forces udp/443 → TCP, so it only helps
	// when QUIC itself is stalling. A dead-ratio driven purely by one-way VOICE on
	// RTC ports (19294-19344/50000-50100) carries DeadQUICFlows==0 — that condition
	// is self-healing (WedgedOneWayRTC) and reject-quic cannot fix it (LOT-35).
	DeadQUICFlows int
	// FrozenFlows is matched flows (TCP or UDP) whose cumulative bytes barely
	// advanced between passes while stuck at a small total — the TSPU IP-throttle
	// freeze signature (LOT-43). Unlike DeadUDPFlows (UDP-only, single-snapshot,
	// 0-download), this catches a TCP fallback frozen mid-stream at a few KB.
	FrozenFlows int
	// OneWayUDPFlows is matched UDP flows that are SENDING but getting nothing back
	// (upload > 0, download ~0) — distinct from DeadUDPFlows, which also counts fully
	// idle flows. This is the wedged-RTC / one-way-voice signature (LOT-35 #2): after
	// a restart tears the call's conntrack/NAT, the client keeps transmitting RTP /
	// retrying the handshake to several media IPs while the return path stays dead.
	OneWayUDPFlows int
	// Bytes is total upload+download across matched flows.
	Bytes int64

	// DestIPs is the set of distinct, parseable destination IPs observed across
	// this service's matched flows. It feeds the iplearn Learner so the
	// remediation planner's ip-fallback rung gets real CDN CIDRs. Deduplicated
	// per pass; only real (net.ParseIP-able) addresses are collected.
	DestIPs []net.IP

	// LeakRatio = LeakFlows / Flows (0 when Flows == 0).
	LeakRatio float64
	// DeadFlowRatio = DeadUDPFlows / UDPFlows (0 when UDPFlows == 0).
	DeadFlowRatio float64
	// OneWayUDPRatio = OneWayUDPFlows / UDPFlows (0 when UDPFlows == 0).
	OneWayUDPRatio float64
	// StalledRatio = FrozenFlows / Flows (0 when Flows == 0) — the throttle-freeze ratio (LOT-43).
	StalledRatio float64
}

// rtcMinUDPFlows / rtcOneWayRatio gate the wedged-one-way-RTC surface signal: at
// least this many UDP flows, most of them sending-without-receiving. Heuristic
// thresholds, not a remediation gate — they only drive a log/metric (LOT-35 #2).
const (
	rtcMinUDPFlows = 4
	rtcOneWayRatio = 0.5
)

// WedgedOneWayRTC reports whether this service shows the wedged one-way RTC
// signature: enough UDP flows, most of them sending with no return (OneWayUDPRatio
// over threshold). It is SURFACE-ONLY — only the client can recover a torn voice
// session by renegotiating, so this never feeds remediation; it just makes the
// condition visible in logs/metrics (LOT-35 #2).
func (m ServiceMetrics) WedgedOneWayRTC() bool {
	return m.UDPFlows >= rtcMinUDPFlows && m.OneWayUDPRatio > rtcOneWayRatio
}

// Snapshot is the result of one eye pass: per-service metrics plus the totals
// it scanned (Matched + Unmatched == total connections seen).
type Snapshot struct {
	Services  map[string]ServiceMetrics
	Matched   int
	Unmatched int
}

// HasLiveRealtimeUDP reports whether any service currently has a LIVE (non-dead)
// UDP/QUIC flow — at least one matched UDP connection still moving bytes — and the
// name of one such service. This is the voice/RTC signal the reconciler uses to
// defer a sing-box restart that would tear an active call's UDP conntrack/NAT
// (LOT-35): a restart mid-call drops the voice session until the client renegotiates.
// It is self-correcting — a wedged path carries only DEAD UDP flows (download ~0,
// counted in DeadUDPFlows), so a genuinely broken service does NOT block the restart
// that would recover it; only a healthy, in-progress flow does.
func (s Snapshot) HasLiveRealtimeUDP() (string, bool) {
	for name, m := range s.Services {
		if m.UDPFlows-m.DeadUDPFlows > 0 {
			return name, true
		}
	}
	return "", false
}

// matcher precompiles a service's match inputs: its route-selector target, plus
// lowercased domain suffixes and parsed IP CIDRs (the heuristic fallback).
type matcher struct {
	svc       registry.Service
	routeFrag string // "route(sel-<svc>)" — sing-box's own decision (authoritative)
	suffixes  []string
	cidrs     []*net.IPNet
}

// matchesSelector reports whether sing-box ROUTED this connection to the service's
// selector (parsed from the rule string). This is authoritative — it is sing-box's
// own routing decision — and attributes rule_set-routed flows the host/IP heuristic
// cannot see (e.g. www.youtube.com via geosite-youtube). LOT-20.
func (m matcher) matchesSelector(c Conn) bool {
	return c.Rule != "" && strings.Contains(c.Rule, m.routeFrag)
}

// matchesHeuristic is the fallback for flows with no selector route (direct/final):
// host ends with a domain suffix (at a label boundary), or dest IP is in a CIDR.
func (m matcher) matchesHeuristic(c Conn) bool {
	host := strings.ToLower(strings.TrimSuffix(c.Host, "."))
	for _, suf := range m.suffixes {
		if host == suf || strings.HasSuffix(host, "."+suf) {
			return true
		}
	}
	if c.DestIP != "" {
		if ip := net.ParseIP(c.DestIP); ip != nil {
			for _, n := range m.cidrs {
				if n.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}

// Stall detection (LOT-43): a flow is "frozen" when its cumulative byte count
// barely advances between passes while it sits stuck at a small total — the
// TSPU IP-throttle signature (a connection establishes, moves a few KB, then
// freezes until timeout; the app churns new ones that freeze too). A header-only
// probe and the UDP-only dead-flow detector both miss this; watching byte deltas
// across passes catches it for TCP and UDP alike.
const (
	frozenDeltaMax int64 = 256    // <this many bytes of progress between passes = "not advancing"
	stuckMinBytes  int64 = 2048   // past the handshake (it WAS transferring), so not just an idle keepalive
	stuckMaxBytes  int64 = 131072 // under a real sustained transfer (a completed/large download is not "stuck")

	// frozenPasses is how many CONSECUTIVE passes a flow must fail to advance
	// before it counts as frozen. One pass was not evidence: `social` was declared
	// TSPU-throttled for nine minutes while Instagram served 392 KiB through the
	// same path, because Instagram's push gateway is a long-lived socket that moves
	// a few KB and then waits for notifications — motionless is its JOB, and at
	// 6 600 bytes it sits three times over stuckMinBytes, deep inside the band that
	// was supposed to exclude keep-alives (LOT-52).
	//
	// Three passes ≈ 90 seconds of not one byte. A push channel that receives
	// anything at all in that window drops out; the TSPU freeze holds its
	// connection until timeout, which is minutes, so the signature this detector
	// exists for still lands. It is a stricter reading of the same evidence rather
	// than a different signal — which is why it cannot miss a freeze that a single
	// pass would have caught, only delay it by a minute.
	frozenPasses = 3
)

// Eye computes observed metrics from the live connection table.
type Eye struct {
	mu       sync.Mutex
	src      Source
	matchers []matcher
	prev     map[string]flowSeen // connID -> bytes last pass + how long it has not advanced (LOT-43/LOT-52)
}

// New builds an Eye over a connection source and the service registry. Every
// service gets a matcher: the PRIMARY signal is sing-box's own route decision —
// the "route(sel-<svc>)" target in the connection's rule string — which attributes
// rule_set-routed flows (e.g. www.youtube.com via geosite-youtube) that inline
// Domains/IPs cannot see (LOT-20). Domains (suffix) and IPs (CIDR) remain as the
// heuristic fallback for direct/final flows that carry no selector route.
func New(src Source, reg *registry.Registry) *Eye {
	e := &Eye{src: src, prev: map[string]flowSeen{}}
	for _, svc := range reg.Services {
		m := matcher{svc: svc, routeFrag: "route(" + registry.SelectorTag(svc.Name) + ")"}
		for _, d := range svc.Domains {
			if d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), ".")); d != "" {
				m.suffixes = append(m.suffixes, d)
			}
		}
		for _, c := range svc.IPs {
			c = strings.TrimSpace(c)
			if _, n, err := net.ParseCIDR(c); err == nil {
				m.cidrs = append(m.cidrs, n)
			} else if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				m.cidrs = append(m.cidrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			}
		}
		e.matchers = append(e.matchers, m) // selector matching works for every service
	}
	return e
}

// Observe fetches the live connection table and computes per-service metrics. A
// connection is attributed to the first matching service. Only tunnel-intended
// services (VPN-preferred) count "direct" flows as leaks; zapret/direct-preferred
// services route direct by design, so direct IS a legitimate path for them.
func (e *Eye) Observe(ctx context.Context) (Snapshot, error) {
	conns, err := e.src.Connections(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := make(map[string]flowSeen, len(conns)) // this pass's per-conn bytes + stall streak; becomes prev
	snap := Snapshot{Services: map[string]ServiceMetrics{}}
	// seenIP dedups destination IPs per service within this pass, so DestIPs holds
	// each distinct address once even when many flows share a CDN edge.
	seenIP := map[string]map[string]bool{}
	for _, c := range conns {
		// Two-tier: sing-box's own route decision (selector) is authoritative; the
		// host/IP heuristic is the fallback for direct/final flows with no route tag.
		var hit *matcher
		for i := range e.matchers {
			if e.matchers[i].matchesSelector(c) {
				hit = &e.matchers[i]
				break
			}
		}
		if hit == nil {
			for i := range e.matchers {
				if e.matchers[i].matchesHeuristic(c) {
					hit = &e.matchers[i]
					break
				}
			}
		}
		if hit == nil {
			snap.Unmatched++
			continue
		}
		snap.Matched++

		sm := snap.Services[hit.svc.Name]
		sm.Service = hit.svc.Name
		sm.Flows++
		sm.Bytes += c.Upload + c.Download
		if c.DestIP != "" {
			if ip := net.ParseIP(c.DestIP); ip != nil {
				seen := seenIP[hit.svc.Name]
				if seen == nil {
					seen = map[string]bool{}
					seenIP[hit.svc.Name] = seen
				}
				if key := ip.String(); !seen[key] {
					seen[key] = true
					sm.DestIPs = append(sm.DestIPs, ip)
				}
			}
		}
		if hit.svc.TunnelIntended() && c.finalOutbound() == directOutbound {
			sm.LeakFlows++
		}
		if isUDP(c.Network) {
			sm.UDPFlows++
			if c.Download <= deadDownloadBytes {
				sm.DeadUDPFlows++
				if c.DestPort == quicPort {
					sm.DeadQUICFlows++ // a stalled QUIC flow — the thing reject-quic actually fixes
				}
				if c.Upload > 0 {
					sm.OneWayUDPFlows++ // sending but nothing back = wedged RTC (LOT-35 #2)
				}
			}
		}
		// Stall/freeze detection (LOT-43): a flow stuck at a small total whose bytes
		// did not advance since last pass = the TSPU IP-throttle freeze.
		if c.ID != "" {
			total := c.Upload + c.Download
			now := flowSeen{total: total}
			if p, seen := e.prev[c.ID]; seen {
				if d := total - p.total; d < frozenDeltaMax && d > -frozenDeltaMax {
					now.still = p.still + 1
				}
				// Any real progress resets the streak: this is about a flow that STOPPED
				// and stayed stopped, not one that is merely quiet right now.
				if now.still >= frozenPasses && total >= stuckMinBytes && total <= stuckMaxBytes {
					sm.FrozenFlows++
				}
			}
			cur[c.ID] = now
		}
		snap.Services[hit.svc.Name] = sm
	}

	for name, sm := range snap.Services {
		if sm.Flows > 0 {
			sm.LeakRatio = float64(sm.LeakFlows) / float64(sm.Flows)
			sm.StalledRatio = float64(sm.FrozenFlows) / float64(sm.Flows)
		}
		if sm.UDPFlows > 0 {
			sm.DeadFlowRatio = float64(sm.DeadUDPFlows) / float64(sm.UDPFlows)
			sm.OneWayUDPRatio = float64(sm.OneWayUDPFlows) / float64(sm.UDPFlows)
		}
		snap.Services[name] = sm
	}
	e.prev = cur // remember this pass's byte counts for next-pass stall detection
	return snap, nil
}

func isUDP(network string) bool {
	return strings.EqualFold(strings.TrimSpace(network), "udp")
}

// flowSeen is what the eye remembers about one connection between passes: where
// its byte counter stood, and how many consecutive passes it has failed to move.
// The streak is the part that distinguishes a throttled flow from a keep-alive —
// both are motionless in any single sample, and only one of them stays that way.
type flowSeen struct {
	total int64 // cumulative bytes at the last pass
	still int   // consecutive passes with no meaningful progress
}
