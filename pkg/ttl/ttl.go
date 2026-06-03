// Package ttl estimates the number of hops to the DPI (TSPU) so bypass
// strategies can set an accurate fake-packet TTL: the fake/desync packet must
// travel far enough to reach the DPI's inspection point but expire before the
// real server, or the server sees the junk. youtubeUnblock calls this
// --fake-sni-ttl; zapret calls it --dpi-desync-ttl / autottl.
//
// The math here is pure. Capturing the TTL of the DPI's injected RST (the most
// reliable signal) needs a raw socket on the router and is done elsewhere; the
// traceroute parser is a portable fallback.
package ttl

import (
	"strings"
)

// standardInitialTTLs are the common IP TTL start values by OS/stack.
var standardInitialTTLs = []int{64, 128, 255}

// GuessInitialTTL returns the standard initial TTL a packet with the observed
// TTL most likely started from (the smallest standard value >= observed).
func GuessInitialTTL(observed int) int {
	for _, t := range standardInitialTTLs {
		if observed <= t {
			return t
		}
	}
	return 255
}

// HopsAway returns how many hops a packet travelled given its observed TTL —
// i.e. the distance to whatever sent it (e.g. the DPI that injected an RST).
func HopsAway(observedTTL int) int {
	if observedTTL <= 0 {
		return 0
	}
	h := GuessInitialTTL(observedTTL) - observedTTL
	if h < 0 {
		return 0
	}
	return h
}

// RecommendDesyncTTL returns the TTL to give a fake/desync packet so it reaches
// a DPI hopsToDPI away but expires before the destination. It is hopsToDPI
// itself (the fake dies at the DPI's hop), with a floor of 1.
func RecommendDesyncTTL(hopsToDPI int) int {
	if hopsToDPI < 1 {
		return 1
	}
	return hopsToDPI
}

// ParseTraceroute counts the responding hops in `traceroute`/`tracepath`
// output — a portable distance estimate when the RST-TTL method is unavailable.
// Lines that are pure timeouts ("* * *") are still hops but unidentified; lines
// with an address count as reached hops.
func ParseTraceroute(output string) (reachedHops int) {
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// A hop line starts with a hop number.
		if !isNumber(f[0]) {
			continue
		}
		// Count it as reached if any field looks like an IP/host (contains a dot
		// or colon) rather than only "*".
		reached := false
		for _, tok := range f[1:] {
			if strings.ContainsAny(tok, ".:") && tok != "*" {
				reached = true
				break
			}
		}
		if reached {
			reachedHops++
		}
	}
	return reachedHops
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
