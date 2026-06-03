package dataplane

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/quality"
)

// Probe types. HTTP measures app-layer reachability; TCP measures whether a
// handshake completes (cheap, catches RST/timeout from DPI); STUN sends a real
// STUN Binding Request over UDP and waits for a response — the right signal for
// UDP/voice paths (Discord voice, WebRTC) that an HTTP probe cannot see.
const (
	ProbeHTTP = "http"
	ProbeTCP  = "tcp"
	ProbeSTUN = "stun" // UDP reachability via STUN
)

// ServiceProbe declares how to probe one service.
type ServiceProbe struct {
	Type   string // ProbeHTTP | ProbeTCP | ProbeSTUN
	Target string // http: URL; tcp/stun: host:port
}

// MultiProber probes each service with its configured probe type.
type MultiProber struct {
	specs   map[string]ServiceProbe
	http    *HTTPProber
	timeout time.Duration
	dialer  *socks5Dialer // nil = probe direct from the box
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
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DialContext: m.dialer.DialContext},
	}
	return m
}

func (m *MultiProber) Probe(ctx context.Context, service string, position int) events.ProductionVerdict {
	sp := m.specs[service]
	switch sp.Type {
	case ProbeTCP:
		return m.probeTCP(ctx, service, position, sp.Target)
	case ProbeSTUN:
		return m.probeSTUN(ctx, service, position, sp.Target)
	default: // ProbeHTTP or unset
		return m.http.Probe(ctx, service, position)
	}
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
	if m.dialer != nil {
		conn, err = m.dialer.DialContext(ctx, "tcp", target)
	} else {
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

	d := net.Dialer{Timeout: m.timeout}
	conn, err := d.DialContext(ctx, "udp", target)
	if err != nil {
		v.RTTms = int(time.Since(start).Milliseconds())
		v.Err = "dial: " + err.Error()
		return v
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(m.timeout))

	req, txID := stunBindingRequest()
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
	if !isStunResponse(buf[:n], txID) {
		v.Err = "malformed STUN response"
		return v
	}
	v.OK = true
	return v
}

// stunBindingRequest builds a 20-byte RFC 5389 Binding Request and returns it
// with its transaction ID.
func stunBindingRequest() ([]byte, [12]byte) {
	var tx [12]byte
	rand.Read(tx[:])
	msg := make([]byte, 20)
	// message type 0x0001 (Binding Request), length 0x0000
	msg[0], msg[1] = 0x00, 0x01
	msg[2], msg[3] = 0x00, 0x00
	// magic cookie 0x2112A442
	msg[4], msg[5], msg[6], msg[7] = 0x21, 0x12, 0xA4, 0x42
	copy(msg[8:20], tx[:])
	return msg, tx
}

func isStunResponse(b []byte, tx [12]byte) bool {
	if len(b) < 20 {
		return false
	}
	// magic cookie present and transaction ID echoed back.
	if b[4] != 0x21 || b[5] != 0x12 || b[6] != 0xA4 || b[7] != 0x42 {
		return false
	}
	for i := 0; i < 12; i++ {
		if b[8+i] != tx[i] {
			return false
		}
	}
	return true
}
