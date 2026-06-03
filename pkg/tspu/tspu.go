// Package tspu classifies how a service is being blocked from the probe's
// failure mode, so Lotsman can jump straight to the mechanism that addresses it
// instead of blindly walking the fallback chain. A TCP reset after the
// handshake is DPI (zapret's job); a timeout/blackhole is an IP-level drop (VPN
// tunnels past it); a fake DNS answer is poisoning (DNS, not routing); a huge
// RTT is throttling. Pure classification — no network here.
package tspu

import (
	"net"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategy"
)

// BlockType is the inferred cause of a probe outcome.
type BlockType string

const (
	None      BlockType = "none"       // reachable, healthy
	DNSPoison BlockType = "dns_poison" // resolver returned a sinkhole/fake IP
	TCPReset  BlockType = "tcp_reset"  // RST mid-handshake — DPI
	Timeout   BlockType = "timeout"    // dropped/blackholed — IP block
	Throttle  BlockType = "throttle"   // reachable but abnormally slow
	Refused   BlockType = "refused"    // connection refused — dest down, not a block
	Unknown   BlockType = "unknown"
)

// fakeRanges are IPs a poisoning resolver hands back instead of the real one.
var fakeRanges = mustCIDRs(
	"192.0.2.0/24",   // TEST-NET-1, common sinkhole (seen on this network)
	"198.18.0.0/15",  // sing-box default fakeip
	"0.0.0.0/32",
	"127.0.0.0/8",    // loopback as a public answer = poisoning
)

// Signals are what the prober observed.
type Signals struct {
	OK          bool
	Err         string  // probe error text
	ResolvedIP  string  // IP the target resolved to (optional)
	RTTms       int
	BaselineRTT float64 // normal RTT for this service (0 = unknown)
}

// Classify infers the block type from probe signals.
func Classify(s Signals) BlockType {
	// DNS poisoning is detectable regardless of the connection result.
	if ip := net.ParseIP(s.ResolvedIP); ip != nil {
		for _, r := range fakeRanges {
			if r.Contains(ip) {
				return DNSPoison
			}
		}
	}

	if s.OK {
		if s.BaselineRTT > 0 && float64(s.RTTms) > s.BaselineRTT*5 {
			return Throttle
		}
		return None
	}

	e := strings.ToLower(s.Err)
	switch {
	case strings.Contains(e, "reset"):
		return TCPReset
	case strings.Contains(e, "refused"):
		return Refused
	case strings.Contains(e, "timeout"), strings.Contains(e, "deadline"), strings.Contains(e, "i/o timeout"):
		return Timeout
	default:
		return Unknown
	}
}

// SuggestClass maps a block type to the strategy class that addresses it, or ""
// when no routing change helps (refused = dest down; dns_poison needs a DNS fix,
// not a route). Brain can use this to skip straight to the right chain step.
func SuggestClass(b BlockType) string {
	switch b {
	case TCPReset:
		return strategy.ClassZapret // DPI reset -> DPI bypass
	case Timeout, Throttle:
		return strategy.ClassVPN // IP drop / throttle -> tunnel
	case None:
		return strategy.ClassDirect
	default:
		return "" // refused / dns_poison / unknown: routing change won't help
	}
}

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}
