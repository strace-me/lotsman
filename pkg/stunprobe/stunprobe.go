// Package stunprobe measures the health of a UDP "voice" path the way HTTP
// probes measure a web path — by sending STUN Binding Requests and counting how
// many come back intact. It exists because voice (Discord RTC, WebRTC) is raw
// UDP you cannot curl: to decide whether a path carries voice — clean-direct, or
// through a desync recipe, or only via VPN — the tester needs a UDP signal, and
// STUN is the exact protocol voice negotiation starts with (and the exact thing
// nfqws's `--filter-l7=stun` desync mangles). So a STUN probe on the voice ports
// reveals whether the path passes voice-shaped UDP uncorrupted.
//
// The packet build/validate are pure (unit-tested); Probe adds the thin UDP I/O
// and reports a quality.Quality (loss + latency tail) the tester already speaks.
package stunprobe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"time"

	"github.com/strace-me/lotsman/pkg/quality"
)

// magicCookie is the fixed STUN magic cookie (RFC 5389 §6) — a response that
// echoes it (and our transaction ID) proves the exchange survived the path.
const magicCookie uint32 = 0x2112A442

const (
	bindingRequest uint16 = 0x0001
	headerLen             = 20
)

// DefaultServer is a widely-reachable public STUN server (Google). The port
// 19302 falls inside a typical nfqws voice-desync range, so a probe here is
// subject to the same mangling as real voice. The live caller overrides this
// with a discord voice server / a target on the actual voice port when testing
// a specific service.
const DefaultServer = "stun.l.google.com:19302"

// BuildBindingRequest crafts a 20-byte STUN Binding Request carrying txID. Pure.
func BuildBindingRequest(txID [12]byte) []byte {
	msg := make([]byte, headerLen)
	binary.BigEndian.PutUint16(msg[0:2], bindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // message length: no attributes
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txID[:])
	return msg
}

// IsBindingResponse reports whether resp is a valid STUN response to a request
// with txID: it must be at least a header long, be flagged as a response (not a
// request), and echo our magic cookie and transaction ID. A dropped or
// desync-corrupted packet will not satisfy this, so it cleanly distinguishes a
// surviving voice path from a broken one. Pure.
func IsBindingResponse(resp []byte, txID [12]byte) bool {
	if len(resp) < headerLen {
		return false
	}
	if binary.BigEndian.Uint32(resp[4:8]) != magicCookie {
		return false
	}
	for i := 0; i < 12; i++ {
		if resp[8+i] != txID[i] {
			return false
		}
	}
	// Top two bits of the message type are always 0 in STUN; a response has the
	// class bit (0x0100) set. Requests (0x0001) would not — reject our own echo.
	mt := binary.BigEndian.Uint16(resp[0:2])
	return mt&0xC000 == 0 && mt&0x0100 != 0
}

// Probe sends `samples` STUN Binding Requests to server over UDP and returns the
// quality of the exchange: each request gets a fresh transaction ID, and a reply
// counts only if it is a valid response to THAT request (so corruption shows as
// loss). RTTs of the successful ones give the latency tail. The probe egresses
// the host normally, so on the router it traverses the same nft/nfqws path as
// real voice — which is the point.
func Probe(ctx context.Context, server string, samples int, timeout time.Duration) quality.Quality {
	if samples < 1 {
		samples = 1
	}
	rtts := make([]float64, 0, samples)
	for i := 0; i < samples; i++ {
		if err := ctx.Err(); err != nil {
			break
		}
		if rtt, ok := probeOnce(server, timeout); ok {
			rtts = append(rtts, rtt)
		}
	}
	return quality.FromRTTs(rtts, samples)
}

func probeOnce(server string, timeout time.Duration) (float64, bool) {
	var tx [12]byte
	if _, err := rand.Read(tx[:]); err != nil {
		return 0, false
	}
	conn, err := net.DialTimeout("udp", server, timeout)
	if err != nil {
		return 0, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	start := time.Now()
	if _, err := conn.Write(BuildBindingRequest(tx)); err != nil {
		return 0, false
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, false
	}
	if !IsBindingResponse(buf[:n], tx) {
		return 0, false
	}
	return float64(time.Since(start).Milliseconds()), true
}
