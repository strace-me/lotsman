// Package bypasslearn is the TM-5 learned-bypass core: it watches live flows and
// learns which destinations are NAT-sensitive (game-UDP, voice/RTC) so they can
// be added to the treatment=bypass set automatically — the Eye-fed analogue of
// iplearn, by traffic CLASS, never by device.
//
// A NAT-sensitive flow is UDP, non-443 (QUIC web is a VPN concern, not bypass),
// to a routable non-excluded destination, and BIDIRECTIONAL (real upload AND
// download — a one-way or dead flow is not a session worth opening NAT for). The
// classifier is pure; the learner promotes a destination only after it is seen
// repeatedly, so a one-off does not widen the bypass set.
package bypasslearn

import (
	"net"
	"sort"
	"strconv"
	"sync"
)

// Classifier decides whether one flow is a NAT-sensitive bypass candidate. Pure;
// the Eye adapts live connections into its primitive inputs.
type Classifier struct {
	Exclude      []*net.IPNet // dst in these (private/VPN/RU) is NOT a candidate
	MinPort      int          // high-port floor (<=0 => 1024); below it = not bypass
	ExcludePorts map[int]bool // dports never bypassed (nil => {443})
}

// Candidate reports the destination IP to bypass for this flow, and whether it
// qualifies. network/dstIP/dstPort are the flow's L3/L4; upload/download are its
// byte counters (bidirectional = a real session).
func (c Classifier) Candidate(network, dstIP, dstPort string, upload, download int64) (net.IP, bool) {
	if !equalFoldUDP(network) {
		return nil, false // only UDP (games/voice/P2P); TCP desync/route is handled elsewhere
	}
	if upload <= 0 || download <= 0 {
		return nil, false // one-way or dead flow — not a NAT-sensitive session
	}
	port, err := strconv.Atoi(dstPort)
	if err != nil || port <= 0 {
		return nil, false
	}
	if c.excludedPort(port) {
		return nil, false
	}
	floor := c.MinPort
	if floor <= 0 {
		floor = 1024
	}
	if port < floor {
		return nil, false // low/well-known ports (DNS 53, etc.) are not game/RTC
	}
	ip := net.ParseIP(dstIP)
	if ip == nil || !routable(ip) {
		return nil, false
	}
	for _, n := range c.Exclude {
		if n != nil && n.Contains(ip) {
			return nil, false // RU/VPN/private destination — not a bypass candidate
		}
	}
	return ip, true
}

func (c Classifier) excludedPort(port int) bool {
	if c.ExcludePorts == nil {
		return port == 443 // default: QUIC web (443) is a VPN concern, not bypass
	}
	return c.ExcludePorts[port]
}

// routable rejects loopback/link-local/multicast/unspecified (private RFC1918 is
// caught by the caller's Exclude set, but these are never bypass destinations).
func routable(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

func equalFoldUDP(network string) bool {
	return network == "udp" || network == "UDP" || network == "Udp"
}

// Learner accumulates classified candidates and promotes a destination to the
// bypass set after it has been observed Promote times (so a one-off does not
// widen the set). Safe for concurrent use.
type Learner struct {
	Promote int // observations before promotion (<=0 => 2)

	mu   sync.Mutex
	seen map[string]int
}

// Observe records one candidate destination (call only with Classifier-accepted IPs).
func (l *Learner) Observe(ip net.IP) {
	if ip == nil {
		return
	}
	l.mu.Lock()
	if l.seen == nil {
		l.seen = map[string]int{}
	}
	l.seen[ip.String()]++
	l.mu.Unlock()
}

// Snapshot returns the promoted bypass destinations (seen >= Promote), sorted.
func (l *Learner) Snapshot() []net.IP {
	l.mu.Lock()
	defer l.mu.Unlock()
	need := l.Promote
	if need <= 0 {
		need = 2
	}
	var out []net.IP
	for s, n := range l.seen {
		if n >= need {
			if ip := net.ParseIP(s); ip != nil {
				out = append(out, ip)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
