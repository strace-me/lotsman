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
// the DNS + SNI-based routing, exactly as for forwarded traffic. UDP is
// supported via UDP ASSOCIATE (UDPRoundTrip) so STUN probes traverse the actual
// tunnel (the service's VPN pool), not the box's direct egress.
type socks5Dialer struct {
	proxy   string // host:port of the SOCKS5 server
	timeout time.Duration
}

// socksAddr appends a SOCKS5 address (ATYP + ADDR + PORT) for host:port to b:
// an IP literal uses ATYP 1/4, else a domain (ATYP 3) the proxy resolves.
func socksAddr(b []byte, host string, port int) ([]byte, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			b = append(b, 0x01)
			b = append(b, v4...)
		} else {
			b = append(b, 0x04)
			b = append(b, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("socks5: hostname too long")
		}
		b = append(b, 0x03, byte(len(host)))
		b = append(b, host...)
	}
	return append(b, byte(port>>8), byte(port)), nil
}

// addrLen returns the byte length of a SOCKS address (ATYP + ADDR + PORT) that
// starts at b[0]=ATYP, b[1:]=ADDR(+PORT). Used to skip the reply/datagram header.
func addrLen(b []byte) (int, error) {
	if len(b) < 1 {
		return 0, fmt.Errorf("socks5: short addr")
	}
	switch b[0] {
	case 0x01:
		return 1 + 4 + 2, nil
	case 0x04:
		return 1 + 16 + 2, nil
	case 0x03:
		if len(b) < 2 {
			return 0, fmt.Errorf("socks5: short domain addr")
		}
		return 1 + 1 + int(b[1]) + 2, nil
	default:
		return 0, fmt.Errorf("socks5: bad atyp %d", b[0])
	}
}

// UDPRoundTrip sends one datagram (payload) to dst (host:port) through the proxy
// via SOCKS5 UDP ASSOCIATE (RFC 1928 §7) and returns the response payload. The
// proxy relays the datagram through sing-box's routing — so a STUN probe sent
// this way traverses the service's actual VPN pool, the representative UDP path,
// not the box's direct egress. The TCP control conn is held open for the life of
// the association (closing it tears the UDP relay down).
func (s *socks5Dialer) UDPRoundTrip(ctx context.Context, dst string, payload []byte) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(dst)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("socks5: bad port %q: %w", portStr, err)
	}

	ctrl, err := (&net.Dialer{Timeout: s.timeout}).DialContext(ctx, "tcp", s.proxy)
	if err != nil {
		return nil, err
	}
	defer ctrl.Close() // closing the control conn ends the UDP association
	dl := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	ctrl.SetDeadline(dl)

	// No-auth greeting.
	if _, err := ctrl.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return nil, err
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(ctrl, rep); err != nil {
		return nil, err
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		return nil, fmt.Errorf("socks5: no-auth rejected (%#x %#x)", rep[0], rep[1])
	}

	// UDP ASSOCIATE request; client addr 0.0.0.0:0 (we accept relay from any).
	if _, err := ctrl.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(ctrl, head); err != nil {
		return nil, err
	}
	if head[1] != 0x00 {
		return nil, fmt.Errorf("socks5: associate failed (rep=%d)", head[1])
	}
	// Relay address: BND.ADDR + BND.PORT.
	var bnd net.IP
	switch head[3] {
	case 0x01:
		bnd = make(net.IP, 4)
	case 0x04:
		bnd = make(net.IP, 16)
	default:
		return nil, fmt.Errorf("socks5: associate reply atyp %d unsupported", head[3])
	}
	if _, err := io.ReadFull(ctrl, bnd); err != nil {
		return nil, err
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(ctrl, pb); err != nil {
		return nil, err
	}
	relayHost := bnd.String()
	if bnd.IsUnspecified() { // 0.0.0.0 => use the proxy's host
		relayHost, _, _ = net.SplitHostPort(s.proxy)
	}
	relayAddr := net.JoinHostPort(relayHost, strconv.Itoa(int(pb[0])<<8|int(pb[1])))

	uconn, err := (&net.Dialer{Timeout: s.timeout}).DialContext(ctx, "udp", relayAddr)
	if err != nil {
		return nil, err
	}
	defer uconn.Close()
	uconn.SetDeadline(dl)

	// Datagram: RSV(2)=0 FRAG(1)=0 ATYP DST.ADDR DST.PORT payload.
	dg := []byte{0, 0, 0}
	dg, err = socksAddr(dg, host, port)
	if err != nil {
		return nil, err
	}
	dg = append(dg, payload...)
	if _, err := uconn.Write(dg); err != nil {
		return nil, err
	}

	buf := make([]byte, 1500)
	n, err := uconn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n < 4 {
		return nil, fmt.Errorf("socks5: short udp reply")
	}
	al, err := addrLen(buf[3:n])
	if err != nil {
		return nil, err
	}
	off := 3 + al
	if off > n {
		return nil, fmt.Errorf("socks5: truncated udp reply header")
	}
	return buf[off:n], nil
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
