package dataplane

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ProductionResolver returns a resolver that queries the SAME DNS production uses:
// sing-box's DNS, reached through the tun peer (sentinel). A probe that resolves a
// target here gets the IP production would dial, so a candidate is measured against
// the same IP — and therefore the same bundle profile — production runs.
//
// This is the LOT-77 fix. Without it the sandbox client resolves via the host's
// /etc/resolv.conf (the system resolver, whatever network we are on) while
// production resolves through the tun's sing-box DNS; the two disagree on which
// CDN IP a hostname maps to, a multi-profile bundle (ALT12) then matches a
// different profile for each, and the lane reports a false negative against a
// recipe that works in production.
//
// Returns nil when sentinel is empty (no tun): the caller keeps the system
// resolver, which is the only option without a tunnel.
// DirectResolver returns a resolver that queries a specific DNS server DIRECTLY,
// bypassing our own tun by binding the socket to the physical interface. It is how
// the client probes the Direct resolver, whose whole point is to answer off-VPN: a
// probe through the tun would exercise the VPN's resolver and never notice that the
// direct one has gone dark (LOT-83).
func DirectResolver(server string, iface func() string, timeout time.Duration) *net.Resolver {
	bind := bindToDeviceControl(iface)
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &net.Dialer{Timeout: timeout, Control: bind}
			return d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
		},
	}
}

func ProductionResolver(sentinel string, timeout time.Duration) *net.Resolver {
	if sentinel == "" {
		return nil
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "udp", net.JoinHostPort(sentinel, "53"))
		},
	}
}

// SandboxClient returns an HTTP client whose sockets carry BOTH the sandbox
// fwmark and a source address on the physical interface.
//
// Each half answers a different question and neither is sufficient alone:
//
//   - The MARK is what makes the packet the sandbox's. The production nft table
//     returns marked packets untouched and the sandbox table queues only them, so
//     a marked probe is desynced exactly once, by the CANDIDATE, and the
//     household's traffic never meets it.
//   - The SOURCE ADDRESS keeps the packet on the physical egress path under our
//     own tun. The mark is then visible to the sandbox nft table and the reply
//     returns to the interface that sent it.
//
// Together they make the probe leave on the WAN, meet the sandbox queue, and
// measure the candidate strategy against the live DPI — which is the only way to
// ask "would this recipe work?" without first imposing it on real traffic.
//
// resolver, when non-nil, resolves the probe target the way PRODUCTION does
// (see ProductionResolver) instead of via the host's system resolver. A nil
// resolver keeps the Go default. It is the difference between measuring the IP
// production will dial and measuring whatever CDN IP this host's resolver happens
// to return — the LOT-77 mismatch.
//
// Returns nil where the binding is impossible (non-Linux): a client that silently
// dropped the binding would measure the tunnel and call it the candidate.
func SandboxClient(mark int, iface func() string, timeout time.Duration, resolver *net.Resolver, fingerprint string) *http.Client {
	dial := sandboxDialer(mark, iface, resolver)
	if dial == nil {
		return nil
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:    dial,
			DialTLSContext: uTLSDialContext(dial, fingerprint),
			// Establishing the connection is the thing being measured; a pooled one
			// would present the DPI with nothing and the candidate with no work to do.
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: TLSPhaseTimeout,
		},
	}
}

func sandboxDialer(mark int, iface func() string, resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	if mark == 0 || iface == nil {
		return nil
	}
	ctrl := markControl(mark)
	if ctrl == nil {
		return nil
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		local, err := sandboxLocalAddr(iface, network)
		if err != nil {
			return nil, err
		}
		d := &net.Dialer{
			Timeout:   DialPhaseTimeout,
			Control:   ctrl,
			Resolver:  resolver,
			LocalAddr: local,
		}
		return d.DialContext(ctx, network, addr)
	}
}

func sandboxLocalAddr(iface func() string, network string) (net.Addr, error) {
	name := iface()
	if name == "" {
		return nil, fmt.Errorf("sandbox probe has no egress interface")
	}
	i, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := i.Addrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		if network == "tcp6" || network == "udp6" {
			if ip.To4() == nil {
				return localAddr(ip, network)
			}
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			return localAddr(ip4, network)
		}
	}
	return nil, fmt.Errorf("sandbox interface %q has no address for %s", name, network)
}

func localAddr(ip net.IP, network string) (net.Addr, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return &net.TCPAddr{IP: ip}, nil
	case "udp", "udp4", "udp6":
		return &net.UDPAddr{IP: ip}, nil
	default:
		return nil, fmt.Errorf("unsupported sandbox network %q", network)
	}
}

// DialPhaseTimeout and TLSPhaseTimeout bound the two phases SEPARATELY, and well
// inside the client's overall timeout, so a failure names itself.
//
// With one deadline covering everything, Go reports whichever fired first as
// "Client.Timeout exceeded while awaiting headers" — which is true and useless.
// The two phases mean opposite things here: a TCP handshake that never completes
// says the block is below TLS and no desync recipe can matter, because there will
// be no ClientHello to rewrite; a TLS handshake that dies on an established
// connection is the censor swallowing the hello, which is exactly what a recipe
// rewrites. Split, the transport says `dial tcp …: i/o timeout` for the first and
// `net/http: TLS handshake timeout` for the second, and the verdict carries the
// distinction for free.
//
// Both are generous for a working path and short next to the 20s the caller
// allows for pulling volume: a SYN that has gone unanswered for six seconds on a
// household uplink is not slow, it is blocked.
const (
	DialPhaseTimeout = 6 * time.Second
	TLSPhaseTimeout  = 8 * time.Second
)
