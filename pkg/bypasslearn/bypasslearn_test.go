package bypasslearn

import (
	"net"
	"testing"
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func TestClassifierAcceptsGameUDP(t *testing.T) {
	c := Classifier{Exclude: []*net.IPNet{mustCIDR("10.0.0.0/8"), mustCIDR("45.91.54.162/32")}}
	ip, ok := c.Candidate("udp", "20.190.140.10", "30041", 1200, 4300)
	if !ok || ip.String() != "20.190.140.10" {
		t.Fatalf("game UDP to a routable non-excluded dst should be a candidate, got %v %v", ip, ok)
	}
}

func TestClassifierRejects(t *testing.T) {
	c := Classifier{Exclude: []*net.IPNet{mustCIDR("10.0.0.0/8"), mustCIDR("45.91.0.0/16")}}
	cases := []struct {
		name              string
		network, ip, port string
		up, down          int64
	}{
		{"tcp not udp", "tcp", "20.190.140.10", "30041", 100, 100},
		{"port 443 quic-web", "udp", "20.190.140.10", "443", 100, 100},
		{"low port (dns)", "udp", "20.190.140.10", "53", 100, 100},
		{"one-way (no download)", "udp", "20.190.140.10", "30041", 100, 0},
		{"dead (no upload)", "udp", "20.190.140.10", "30041", 0, 100},
		{"excluded vpn dst", "udp", "45.91.54.162", "30041", 100, 100},
		{"private dst", "udp", "10.1.2.3", "30041", 100, 100},
		{"loopback", "udp", "127.0.0.1", "30041", 100, 100},
		{"bad ip", "udp", "not-an-ip", "30041", 100, 100},
		{"ipv6 dst (ip-family table)", "udp", "2606:4700::1111", "30041", 100, 100},
		{"ipv6 ULA private", "udp", "fd00::1", "30041", 100, 100},
		{"port over 65535", "udp", "20.190.140.10", "70000", 100, 100},
	}
	for _, tc := range cases {
		if _, ok := c.Candidate(tc.network, tc.ip, tc.port, tc.up, tc.down); ok {
			t.Errorf("%s: should be rejected", tc.name)
		}
	}
}

func TestLearnerPromotesAfterThreshold(t *testing.T) {
	l := &Learner{Promote: 3}
	ip := net.ParseIP("20.190.140.10")
	l.Observe(ip)
	l.Observe(ip)
	if len(l.Snapshot()) != 0 {
		t.Fatal("must not promote before threshold (3)")
	}
	l.Observe(ip)
	snap := l.Snapshot()
	if len(snap) != 1 || snap[0].String() != "20.190.140.10" {
		t.Fatalf("must promote at threshold, got %v", snap)
	}
}

func TestLearnerSnapshotSortedAndDeduped(t *testing.T) {
	l := &Learner{Promote: 1}
	for _, s := range []string{"20.190.140.10", "8.8.8.8", "20.190.140.10"} {
		l.Observe(net.ParseIP(s))
	}
	snap := l.Snapshot()
	if len(snap) != 2 || snap[0].String() != "20.190.140.10" || snap[1].String() != "8.8.8.8" {
		// string sort: "20..." < "8..." lexicographically
		t.Fatalf("snapshot must be sorted+deduped, got %v", snap)
	}
}
