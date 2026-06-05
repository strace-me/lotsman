package dataplane

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// fakeSocks5 is a minimal no-auth SOCKS5 server that accepts one CONNECT,
// records the requested target, replies success, then echoes bytes. It lets us
// test socks5Dialer's wire format without a real proxy.
func fakeSocks5(t *testing.T) (addr string, gotTarget chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gotTarget = make(chan string, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// greeting
		g := make([]byte, 3)
		if _, err := io.ReadFull(conn, g); err != nil || g[0] != 0x05 {
			return
		}
		conn.Write([]byte{0x05, 0x00}) // no-auth

		// request: VER CMD RSV ATYP ...
		head := make([]byte, 4)
		if _, err := io.ReadFull(conn, head); err != nil {
			return
		}
		var target string
		switch head[3] {
		case 0x01:
			b := make([]byte, 4+2)
			io.ReadFull(conn, b)
			target = net.IP(b[:4]).String()
		case 0x03:
			l := make([]byte, 1)
			io.ReadFull(conn, l)
			b := make([]byte, int(l[0])+2)
			io.ReadFull(conn, b)
			target = string(b[:int(l[0])])
		case 0x04:
			b := make([]byte, 16+2)
			io.ReadFull(conn, b)
			target = net.IP(b[:16]).String()
		}
		gotTarget <- target

		// success reply with a dummy bound addr 0.0.0.0:0
		conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		// echo
		io.Copy(conn, conn)
	}()
	return ln.Addr().String(), gotTarget
}

func TestSocks5DialerDomain(t *testing.T) {
	addr, gotTarget := fakeSocks5(t)
	d := &socks5Dialer{proxy: addr, timeout: 2 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", "www.youtube.com:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if got := <-gotTarget; got != "www.youtube.com" {
		t.Fatalf("proxy got target %q, want www.youtube.com (hostname must be passed unresolved)", got)
	}

	// data tunnels through (echo server)
	want := []byte("ping")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo got %q, want %q", got, want)
	}
}

func TestSocks5DialerRejectsUDP(t *testing.T) {
	d := &socks5Dialer{proxy: "127.0.0.1:1", timeout: time.Second}
	if _, err := d.DialContext(context.Background(), "udp", "8.8.8.8:53"); err == nil {
		t.Fatal("expected error for udp network")
	}
}

func TestAddrLen(t *testing.T) {
	cases := []struct {
		b    []byte
		want int
	}{
		{[]byte{0x01, 1, 2, 3, 4, 0, 0}, 1 + 4 + 2},           // ipv4
		{[]byte{0x03, 3, 'a', 'b', 'c', 0, 0}, 1 + 1 + 3 + 2}, // domain "abc"
	}
	for _, c := range cases {
		got, err := addrLen(c.b)
		if err != nil || got != c.want {
			t.Errorf("addrLen(%v) = %d,%v want %d", c.b, got, err, c.want)
		}
	}
	if _, err := addrLen([]byte{0x09}); err == nil {
		t.Error("addrLen should reject unknown atyp")
	}
}

// fakeSocks5UDP is a minimal no-auth SOCKS5 UDP ASSOCIATE relay: it accepts the
// TCP control conn, replies with a UDP relay address, then echoes one datagram
// back (same SOCKS header + payload). It records the requested UDP destination.
func fakeSocks5UDP(t *testing.T) (addr string, gotDst chan string) {
	t.Helper()
	tcpln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	uconn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	gotDst = make(chan string, 1)
	go func() {
		defer tcpln.Close()
		defer uconn.Close()
		conn, err := tcpln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		g := make([]byte, 3)
		if _, err := io.ReadFull(conn, g); err != nil || g[0] != 0x05 {
			return
		}
		conn.Write([]byte{0x05, 0x00}) // no-auth

		// ASSOCIATE request: VER CMD RSV ATYP + addr(4)+port(2) for ATYP=1.
		head := make([]byte, 4)
		if _, err := io.ReadFull(conn, head); err != nil || head[1] != 0x03 {
			return
		}
		io.ReadFull(conn, make([]byte, 6))

		// Reply with the relay's real addr so the client dials it directly.
		ra := uconn.LocalAddr().(*net.UDPAddr)
		reply := []byte{0x05, 0x00, 0x00, 0x01}
		reply = append(reply, ra.IP.To4()...)
		reply = append(reply, byte(ra.Port>>8), byte(ra.Port))
		conn.Write(reply)

		// Serve one datagram: parse the SOCKS UDP header, record dst, echo back.
		buf := make([]byte, 1500)
		n, caddr, err := uconn.ReadFromUDP(buf)
		if err != nil || n < 5 {
			return
		}
		// dst host for ATYP=domain (0x03): buf[4]=len, name follows.
		if buf[3] == 0x03 {
			l := int(buf[4])
			gotDst <- string(buf[5 : 5+l])
		} else {
			gotDst <- "non-domain"
		}
		uconn.WriteToUDP(buf[:n], caddr) // echo header+payload verbatim

		io.ReadFull(conn, make([]byte, 1)) // block until client closes control conn
	}()
	return tcpln.Addr().String(), gotDst
}

func TestSocks5UDPRoundTrip(t *testing.T) {
	addr, gotDst := fakeSocks5UDP(t)
	d := &socks5Dialer{proxy: addr, timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	payload := []byte("stun-binding-request")
	resp, err := d.UDPRoundTrip(ctx, "stun.test:3478", payload)
	if err != nil {
		t.Fatalf("UDPRoundTrip: %v", err)
	}
	if string(resp) != string(payload) {
		t.Fatalf("echoed payload = %q, want %q (header must be stripped)", resp, payload)
	}
	if got := <-gotDst; got != "stun.test" {
		t.Fatalf("relay got dst %q, want stun.test (hostname passed unresolved)", got)
	}
}
