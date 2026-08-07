package dataplane

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/quality"
	"github.com/strace-me/lotsman/pkg/stunprobe"
)

// Probe types. HTTP measures app-layer reachability; TCP measures whether a
// handshake completes (cheap, catches RST/timeout from DPI); STUN sends a real
// STUN Binding Request over UDP and waits for a response — the right signal for
// UDP/voice paths (Discord voice, WebRTC) that an HTTP probe cannot see; QUIC
// issues an HTTP/3 request — the right signal for QUIC/video (the "TCP ok, QUIC
// dead" trap that a TCP/HTTP probe is blind to — LOT-3).
const (
	ProbeHTTP = "http"
	ProbeTCP  = "tcp"
	ProbeSTUN = "stun" // UDP reachability via STUN
	ProbeQUIC = "quic" // HTTP/3 reachability (direct box egress — see probeQUIC)
)

// ServiceProbe declares how to probe one service.
type ServiceProbe struct {
	Type   string // ProbeHTTP | ProbeTCP | ProbeSTUN | ProbeQUIC
	Target string // http/quic: URL; tcp/stun: host:port
}

// MultiProber probes each service with its configured probe type.
type MultiProber struct {
	specs     map[string]ServiceProbe
	rungSpecs map[string]map[int]ServiceProbe // per-(service,position) override of specs (LOT-3)
	http      *HTTPProber
	timeout   time.Duration
	dialer    *socks5Dialer // nil = probe direct from the box
	// bound dials straight out on the physical interface (SO_BINDTODEVICE),
	// escaping our own tun. Set only by NewMultiProberBound; nil everywhere else.
	// It takes precedence over the plain dialer for TCP probes for the same reason
	// the HTTP client does: a dial captured by the tun measures the tunnel.
	bound *net.Dialer
}

// OverrideRung sets a probe spec for one service at one chain position, taking
// precedence over the service-level spec for that rung only (LOT-3). Use it to put
// a QUIC probe on the rungs where box-direct HTTP/3 is representative (direct/
// zapret) while leaving the VPN rung on its HTTP probe.
func (m *MultiProber) OverrideRung(service string, position int, sp ServiceProbe) {
	if m.rungSpecs == nil {
		m.rungSpecs = map[string]map[int]ServiceProbe{}
	}
	if m.rungSpecs[service] == nil {
		m.rungSpecs[service] = map[int]ServiceProbe{}
	}
	m.rungSpecs[service][position] = sp
}

// specFor resolves the probe spec for a service at a position: a per-rung override
// if one is set, else the service-level spec.
func (m *MultiProber) specFor(service string, position int) ServiceProbe {
	if byPos, ok := m.rungSpecs[service]; ok {
		if sp, ok := byPos[position]; ok {
			return sp
		}
	}
	return m.specs[service]
}

// NewMultiProber builds a prober from per-service probe specs. Services without
// a spec, or with an empty/unknown type, fall back to HTTP using the spec's
// Target as the URL.
func NewMultiProber(specs map[string]ServiceProbe) *MultiProber {
	targets := map[string]string{}
	for name, sp := range specs {
		targets[name] = sp.Target
	}
	return &MultiProber{specs: specs, http: NewHTTPProber(targets), timeout: 5 * time.Second}
}

// NewMultiProberProxy is like NewMultiProber but routes HTTP and TCP probes
// through a SOCKS5 proxy (sing-box's socks inbound), so probes traverse the
// same VPN/direct+nfqws path as LAN clients. proxyAddr is host:port; empty
// falls back to direct probing. STUN/UDP probes stay direct (no UDP over SOCKS).
func NewMultiProberProxy(specs map[string]ServiceProbe, proxyAddr string) *MultiProber {
	m := NewMultiProber(specs)
	if proxyAddr == "" {
		return m
	}
	m.dialer = &socks5Dialer{proxy: proxyAddr, timeout: m.timeout}
	m.http.Client = &http.Client{
		Timeout: 5 * time.Second,
		// DisableKeepAlives is REQUIRED, not a tuning knob: sing-box routes per
		// CONNECTION, so a pooled connection keeps the outbound it was opened with.
		// Reusing one across a selector flip makes the probe report the PREVIOUS
		// path — observed live as a dead VPN node reporting healthy 26ms replies
		// forever after the brain had escalated away from it, which then triggered a
		// false silent-recovery back onto the dead node. A health probe must open a
		// fresh connection so it measures the route that is in force right now.
		Transport: &http.Transport{DialContext: m.dialer.DialContext, DisableKeepAlives: true},
	}
	return m
}

func (m *MultiProber) Probe(ctx context.Context, service string, position int) events.ProductionVerdict {
	sp := m.specFor(service, position)
	switch sp.Type {
	case ProbeTCP:
		return m.probeTCP(ctx, service, position, sp.Target)
	case ProbeSTUN:
		return m.probeSTUN(ctx, service, position, sp.Target)
	case ProbeQUIC:
		return m.probeQUIC(ctx, service, position, sp.Target)
	default: // ProbeHTTP or unset
		return m.http.Probe(ctx, service, position)
	}
}

// probeQUIC issues an HTTP/3 (QUIC) GET to the target URL. Any HTTP/3 response
// means the QUIC path is open — the signal an HTTP/TCP probe is blind to (the
// "TCP ok, QUIC dead" trap that left youtube's KB healthy while video stalled —
// LOT-3). A fresh transport per probe stops a reused connection from masking a
// fresh block.
//
// CAVEAT — this is DIRECT box egress, NOT the service's active routed path: QUIC
// cannot tunnel through SOCKS5 (UDP ASSOCIATE is one-shot, a QUIC flow is not), so
// unlike probeTCP/probeSTUN it ignores m.dialer. It therefore answers "is QUIC to
// <target> reachable from the box directly?" — meaningful on direct/zapret tiers,
// but it must NOT be a service's only health probe on the VPN tier, where a
// blocked direct QUIC egress is the expected TSPU state and would false-fail.
func (m *MultiProber) probeQUIC(ctx context.Context, service string, position int, target string) events.ProductionVerdict {
	v := events.ProductionVerdict{Service: service, Position: position}
	tr := &http3.Transport{}
	defer tr.Close()
	client := &http.Client{Transport: tr, Timeout: m.timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	start := time.Now()
	resp, err := client.Do(req)
	v.RTTms = int(time.Since(start).Milliseconds())
	if err != nil {
		v.Err = "quic: " + err.Error()
		return v
	}
	resp.Body.Close()
	v.OK = true
	return v
}

// Burst runs count probes spaced by interval and computes tail-latency
// metrics (p50/p95/p99 + jitter + loss) — the right signal for real-time
// services, where the mean hides the spikes. Uses the service's configured
// probe type. ctx cancellation ends the burst early.
func (m *MultiProber) Burst(ctx context.Context, service string, count int, interval time.Duration) quality.Quality {
	okRTTs := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		v := m.Probe(ctx, service, 0)
		if v.OK {
			okRTTs = append(okRTTs, float64(v.RTTms))
		}
		if i < count-1 {
			select {
			case <-ctx.Done():
				return quality.FromRTTs(okRTTs, i+1)
			case <-time.After(interval):
			}
		}
	}
	return quality.FromRTTs(okRTTs, count)
}

func (m *MultiProber) probeTCP(ctx context.Context, service string, position int, target string) events.ProductionVerdict {
	v := events.ProductionVerdict{Service: service, Position: position}
	start := time.Now()
	var (
		conn net.Conn
		err  error
	)
	switch {
	case m.bound != nil:
		conn, err = m.bound.DialContext(ctx, "tcp", target)
	case m.dialer != nil:
		conn, err = m.dialer.DialContext(ctx, "tcp", target)
	default:
		conn, err = (&net.Dialer{Timeout: m.timeout}).DialContext(ctx, "tcp", target)
	}
	v.RTTms = int(time.Since(start).Milliseconds())
	if err != nil {
		v.Err = err.Error()
		return v
	}
	conn.Close()
	v.OK = true
	return v
}

// probeSTUN sends a STUN Binding Request and treats any well-formed STUN reply
// (matching transaction ID) as success — i.e. the UDP path to a STUN server is
// open and not silently dropped by DPI.
func (m *MultiProber) probeSTUN(ctx context.Context, service string, position int, target string) events.ProductionVerdict {
	v := events.ProductionVerdict{Service: service, Position: position}
	start := time.Now()

	var txID [12]byte
	rand.Read(txID[:])
	req := stunprobe.BuildBindingRequest(txID)

	// Through the proxy (representative): the STUN datagram traverses sing-box's
	// UDP routing — the service's actual tunnel/VPN pool — via SOCKS5 UDP
	// ASSOCIATE, the same path real voice/QUIC UDP takes. Direct only when no
	// proxy is configured (then it measures the box's own egress, not the tunnel).
	if m.dialer != nil {
		resp, err := m.dialer.UDPRoundTrip(ctx, target, req)
		v.RTTms = int(time.Since(start).Milliseconds())
		if err != nil {
			v.Err = "stun via proxy: " + err.Error()
			return v
		}
		if !stunprobe.IsBindingResponse(resp, txID) {
			v.Err = "malformed STUN response (via proxy)"
			return v
		}
		v.OK = true
		return v
	}

	d := net.Dialer{Timeout: m.timeout}
	conn, err := d.DialContext(ctx, "udp", target)
	if err != nil {
		v.RTTms = int(time.Since(start).Milliseconds())
		v.Err = "dial: " + err.Error()
		return v
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(m.timeout))

	if _, err := conn.Write(req); err != nil {
		v.RTTms = int(time.Since(start).Milliseconds())
		v.Err = "write: " + err.Error()
		return v
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	v.RTTms = int(time.Since(start).Milliseconds())
	if err != nil {
		v.Err = "no STUN response: " + err.Error()
		return v
	}
	if !stunprobe.IsBindingResponse(buf[:n], txID) {
		v.Err = "malformed STUN response"
		return v
	}
	v.OK = true
	return v
}
