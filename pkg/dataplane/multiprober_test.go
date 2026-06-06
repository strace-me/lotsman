package dataplane

import (
	"context"
	"net"
	"testing"
	"time"
)

func newProber(t *testing.T, specs map[string]ServiceProbe) *MultiProber {
	t.Helper()
	m := NewMultiProber(specs)
	m.timeout = 500 * time.Millisecond // keep failures fast in tests
	return m
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	m := newProber(t, map[string]ServiceProbe{
		"up":   {Type: ProbeTCP, Target: ln.Addr().String()},
		"down": {Type: ProbeTCP, Target: "127.0.0.1:1"}, // refused
	})

	if v := m.Probe(context.Background(), "up", 0); !v.OK {
		t.Errorf("tcp up: expected ok, got err=%q", v.Err)
	}
	if v := m.Probe(context.Background(), "down", 0); v.OK {
		t.Error("tcp down: expected failure on refused port")
	}
}

// LOT-3: the QUIC probe dispatches to HTTP/3 and reports a blocked/unreachable
// QUIC path as a failure (the signal an HTTP/TCP probe misses). A dead UDP target
// has no HTTP/3 responder, so the probe must fail rather than falsely pass.
func TestQUICProbeUnreachable(t *testing.T) {
	m := newProber(t, map[string]ServiceProbe{
		"video": {Type: ProbeQUIC, Target: "https://127.0.0.1:1/"}, // nothing speaks HTTP/3 here
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if v := m.Probe(ctx, "video", 0); v.OK || v.Err == "" {
		t.Errorf("quic to a dead target must fail with an error, got %+v", v)
	}
}

func TestSTUNProbe(t *testing.T) {
	// STUN server that replies with a valid Binding Success.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 20 {
				continue
			}
			resp := make([]byte, 20)
			resp[0], resp[1] = 0x01, 0x01 // Binding Success
			copy(resp[4:20], buf[4:20])   // echo magic cookie + transaction ID
			pc.WriteTo(resp, addr)
		}
	}()

	m := newProber(t, map[string]ServiceProbe{
		"voice": {Type: ProbeSTUN, Target: pc.LocalAddr().String()},
		"dead":  {Type: ProbeSTUN, Target: "127.0.0.1:1"}, // nothing replies
	})

	v := m.Probe(context.Background(), "voice", 0)
	if !v.OK {
		t.Errorf("stun voice: expected ok, got err=%q", v.Err)
	}
	if v := m.Probe(context.Background(), "dead", 0); v.OK {
		t.Error("stun dead: expected failure when no STUN response")
	}
}

func TestSTUNRejectsGarbageResponse(t *testing.T) {
	// Server that replies with non-STUN garbage -> probe must not count it ok.
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer pc.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo([]byte("not a stun response at all!!"), addr)
		}
	}()

	m := newProber(t, map[string]ServiceProbe{"g": {Type: ProbeSTUN, Target: pc.LocalAddr().String()}})
	if v := m.Probe(context.Background(), "g", 0); v.OK {
		t.Error("expected failure on garbage (non-STUN) response")
	}
}
