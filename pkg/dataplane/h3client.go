package dataplane

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// H3Prefix marks a volume target that must be pulled over HTTP/3 rather than
// over TCP. It is a marker on OUR side of the config, not a real URI scheme:
// `h3://www.youtube.com/` fetches `https://www.youtube.com/` over QUIC.
//
// It exists because the transport, not the URL, is what we need to vary. A
// desync recipe for YouTube composes two profiles — one filtered to tcp/80,443
// and one to udp/443 with a QUIC fake — and until now every probe and every
// volume pull in this project went over TCP. The udp/443 profile was therefore
// never measured by anything, on a service whose media is QUIC-first: the recipe
// could be judged good on its TCP half while the half that carries the video was
// dead, which is the "TCP ok, QUIC dead" trap the codebase already had a name for
// (LOT-3) and no instrument for.
const H3Prefix = "h3://"

// IsH3 reports whether a target must be pulled over QUIC.
func IsH3(target string) bool {
	return len(target) > len(H3Prefix) && target[:len(H3Prefix)] == H3Prefix
}

// FetchURL is the URL to actually request for a target, with the h3 marker
// translated back into https.
func FetchURL(target string) string {
	if IsH3(target) {
		return "https://" + target[len(H3Prefix):]
	}
	return target
}

// H3Client returns an HTTP/3 client for direct egress.
func H3Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &h3RoundTripper{timeout: timeout}}
}

// SandboxH3Client is SandboxClient's QUIC twin: a UDP socket carrying BOTH the
// sandbox fwmark and a source address on the physical interface.
//
// Both halves matter exactly as they do for TCP, and the QUIC probe that already
// existed had NEITHER — `probeQUIC` builds a bare `http3.Transport{}`, so its
// packets are unmarked (the sandbox table never sees them, and the production
// table queues them behind whatever the incumbent recipe is) and unbound (under
// our own tun, auto_route pulls them into the tunnel). It says so in its own
// comment, which is honest for a reachability check on a direct rung and fatal
// for a measurement that decides what a rule runs.
//
// Returns nil where the binding is impossible, for the same reason SandboxClient
// does: a measurement that silently lost its isolation describes the tunnel and
// files the verdict against the candidate.
func SandboxH3Client(mark int, iface func() string, timeout time.Duration, resolver *net.Resolver) *http.Client {
	ctrl := markControl(mark)
	if ctrl == nil || iface == nil {
		return nil
	}
	return &http.Client{Timeout: timeout, Transport: &h3RoundTripper{
		ctrl:     ctrl,
		local:    func() (net.Addr, error) { return sandboxLocalAddr(iface, "udp") },
		timeout:  timeout,
		resolver: resolver,
	}}
}

// resolveUDPAddr resolves addr to a UDP address. A non-nil resolver resolves the
// way production does (through the tun's sing-box DNS, see ProductionResolver);
// nil falls back to the system resolver. A literal IP skips resolution either way.
func resolveUDPAddr(ctx context.Context, r *net.Resolver, addr string) (*net.UDPAddr, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if r == nil || net.ParseIP(host) != nil {
		return net.ResolveUDPAddr("udp", addr)
	}
	ips, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, net.UnknownNetworkError("udp: no address for " + host)
	}
	port, err := net.LookupPort("udp", portStr)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ips[0].IP, Port: port}, nil
}

// h3RoundTripper opens a FRESH QUIC connection per request and tears it down
// with the body.
//
// http3.Transport pools connections per host, and a pooled QUIC connection
// presents the censor with nothing: the ClientHello rides the Initial packet of
// the handshake, which is the packet a desync recipe rewrites and a DPI inspects.
// A second request over a live connection therefore measures neither. This is the
// same defect the TCP probe carried for a month behind `fails=0` — it is written
// down as principle 15 — and rebuilding it in the QUIC dimension would be
// unforced.
type h3RoundTripper struct {
	// ctrl stamps the socket with the sandbox mark; nil means plain egress.
	ctrl    func(network, address string, c syscall.RawConn) error
	local   func() (net.Addr, error)
	timeout time.Duration
	// resolver, when non-nil, resolves the target host the way production does
	// (through the tun's sing-box DNS) instead of via the system resolver (LOT-77).
	resolver *net.Resolver
}

func (h *h3RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	listenAddr := ":0"
	if h.local != nil {
		local, err := h.local()
		if err != nil {
			return nil, err
		}
		listenAddr = local.String()
	}
	lc := net.ListenConfig{Control: h.ctrl}
	pc, err := lc.ListenPacket(req.Context(), "udp4", listenAddr)
	if err != nil {
		return nil, err
	}
	udp, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return nil, net.UnknownNetworkError("udp: not a UDP socket")
	}
	qt := &quic.Transport{Conn: udp}
	rt := &http3.Transport{
		Dial: func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
			ua, err := resolveUDPAddr(ctx, h.resolver, addr)
			if err != nil {
				return nil, err
			}
			return qt.DialEarly(ctx, ua, tlsCfg, cfg)
		},
	}
	closeAll := func() {
		rt.Close()
		qt.Close()
		udp.Close()
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		closeAll()
		return nil, err
	}
	// The socket outlives the round trip and dies with the body, because the body
	// is still being read over it. Closing here would truncate the very volume we
	// are measuring and score it as a freeze.
	resp.Body = &closingBody{ReadCloser: resp.Body, done: closeAll}
	return resp, nil
}

type closingBody struct {
	io.ReadCloser
	done func()
}

func (c *closingBody) Close() error {
	err := c.ReadCloser.Close()
	c.done()
	return err
}
