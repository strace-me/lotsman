package dataplane

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// socks5Dialer dials TCP through a no-auth SOCKS5 proxy (RFC 1928, CONNECT).
// It exists so that probes issued from the router itself traverse sing-box's
// socks inbound — i.e. the same routing path (VPN / direct + nfqws) that LAN
// clients take. Without it, box-local probes measure the router's own egress,
// which does not reflect what clients actually experience.
//
// Hostnames are passed to the proxy unresolved (ATYP=domain) so sing-box does
// the DNS + SNI-based routing, exactly as for forwarded traffic. UDP is not
// supported (STUN probes stay direct).
type socks5Dialer struct {
	proxy   string // host:port of the SOCKS5 server
	timeout time.Duration
}

// DialContext connects to addr through the proxy and returns the tunneled conn.
func (s *socks5Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("socks5: unsupported network %q", network)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("socks5: bad port %q: %w", portStr, err)
	}

	conn, err := (&net.Dialer{Timeout: s.timeout}).DialContext(ctx, "tcp", s.proxy)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	// Greeting: VER=5, one method, METHOD=0 (no auth).
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(conn, rep); err != nil {
		conn.Close()
		return nil, err
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5: no-auth rejected (%#x %#x)", rep[0], rep[1])
	}

	// CONNECT request. Prefer IP literal ATYP, else domain (proxy resolves).
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 0x01)
			req = append(req, v4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			conn.Close()
			return nil, fmt.Errorf("socks5: hostname too long")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// Reply: VER, REP, RSV, ATYP, BND.ADDR, BND.PORT.
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		conn.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5: connect failed (rep=%d)", head[1])
	}
	var alen int
	switch head[3] {
	case 0x01:
		alen = 4
	case 0x04:
		alen = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			conn.Close()
			return nil, err
		}
		alen = int(l[0])
	default:
		conn.Close()
		return nil, fmt.Errorf("socks5: bad reply atyp %d", head[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, alen+2)); err != nil {
		conn.Close()
		return nil, err
	}

	conn.SetDeadline(time.Time{}) // hand a clean conn to the caller
	return conn, nil
}
