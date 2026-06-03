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

	"github.com/strace-me/lotsman/pkg/registry"
)

// directOutbound is the sing-box final outbound name for un-tunnelled traffic.
const directOutbound = "direct"

// deadDownloadBytes is the threshold at/below which a matched UDP/QUIC flow is
// "dead": the box uploaded handshake bytes but got ~nothing back (the LOT-2
// conntrack signature was packets=0 bytes=0). Kept at 0 — a strict stall — to
// avoid flagging brief or low-rate flows as dead.
const deadDownloadBytes int64 = 0

// Conn is the subset of a sing-box /connections record the eye needs, decoupled
// from the dataplane HTTP types so observe depends only on registry. main.go
// maps dataplane.Connection into this shape.
//
// Chains is the outbound path: chains[0] is the final outbound ("direct" or a
// node tag). Upload/Download are cumulative bytes. Host is the sniffed SNI (may
// be empty when sniff failed — that flow is matched by DestIP instead).
type Conn struct {
	Chains   []string
	Upload   int64
	Download int64
	Host     string
	DestIP   string
	Network  string // "tcp" | "udp"
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
	// Bytes is total upload+download across matched flows.
	Bytes int64

	// LeakRatio = LeakFlows / Flows (0 when Flows == 0).
	LeakRatio float64
	// DeadFlowRatio = DeadUDPFlows / UDPFlows (0 when UDPFlows == 0).
	DeadFlowRatio float64
}

// Snapshot is the result of one eye pass: per-service metrics plus the totals
// it scanned (Matched + Unmatched == total connections seen).
type Snapshot struct {
	Services  map[string]ServiceMetrics
	Matched   int
	Unmatched int
}

// matcher precompiles a service's match inputs: lowercased domain suffixes and
// parsed IP CIDRs.
type matcher struct {
	svc      registry.Service
	suffixes []string
	cidrs    []*net.IPNet
}

// matches reports whether a connection belongs to this service: host ends with
// a domain suffix (at a label boundary), or destination IP is inside a CIDR.
func (m matcher) matches(c Conn) bool {
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

// Eye computes observed metrics from the live connection table.
type Eye struct {
	src      Source
	matchers []matcher
}

// New builds an Eye over a connection source and the service registry. Services
// with no match inputs (no Domains and no IPs) are skipped: their flows cannot
// be attributed. RuleSets are NOT usable for matching here — they are opaque
// .srs tags resolved inside sing-box, not domain lists we can read — so matching
// is by inline Domains (suffix) and IPs (CIDR) only.
func New(src Source, reg *registry.Registry) *Eye {
	e := &Eye{src: src}
	for _, svc := range reg.Services {
		m := matcher{svc: svc}
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
		if len(m.suffixes) == 0 && len(m.cidrs) == 0 {
			continue
		}
		e.matchers = append(e.matchers, m)
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
	snap := Snapshot{Services: map[string]ServiceMetrics{}}
	for _, c := range conns {
		var hit *matcher
		for i := range e.matchers {
			if e.matchers[i].matches(c) {
				hit = &e.matchers[i]
				break
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
		if hit.svc.TunnelIntended() && c.finalOutbound() == directOutbound {
			sm.LeakFlows++
		}
		if isUDP(c.Network) {
			sm.UDPFlows++
			if c.Download <= deadDownloadBytes {
				sm.DeadUDPFlows++
			}
		}
		snap.Services[hit.svc.Name] = sm
	}

	for name, sm := range snap.Services {
		if sm.Flows > 0 {
			sm.LeakRatio = float64(sm.LeakFlows) / float64(sm.Flows)
		}
		if sm.UDPFlows > 0 {
			sm.DeadFlowRatio = float64(sm.DeadUDPFlows) / float64(sm.UDPFlows)
		}
		snap.Services[name] = sm
	}
	return snap, nil
}

func isUDP(network string) bool {
	return strings.EqualFold(strings.TrimSpace(network), "udp")
}
