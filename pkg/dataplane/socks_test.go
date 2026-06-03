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
