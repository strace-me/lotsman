package dataplane

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// LOT-77: a nil resolver keeps the system resolver (no tun); a sentinel yields a
// resolver that queries the tun peer, so a probe resolves the way production does.
func TestProductionResolverNeedsASentinel(t *testing.T) {
	if r := ProductionResolver("", time.Second); r != nil {
		t.Error("no sentinel must yield the system resolver (nil), not a broken one")
	}
	r := ProductionResolver("172.19.0.2", time.Second)
	if r == nil {
		t.Fatal("a sentinel must yield a resolver that queries it")
	}
	if !r.PreferGo {
		t.Error("the resolver must PreferGo, or the custom Dial is bypassed")
	}
	if r.Dial == nil {
		t.Error("the resolver must carry the tun-peer Dial")
	}
	var _ *net.Resolver = r
}

// The two phases must be bounded separately, or a failure reports itself as
// "Client.Timeout exceeded while awaiting headers" — true, and useless for the
// only question that matters when nothing was delivered: was the block below TLS,
// where no desync recipe can help, or was it the hello being swallowed, which is
// what a recipe rewrites.
func TestTheSandboxClientBoundsEachPhaseSeparately(t *testing.T) {
	c := SandboxClient(0x4554, func() string { return "lo" }, 20*time.Second, nil, "chrome")
	if c == nil {
		t.Skip("no interface binding on this platform")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", c.Transport)
	}
	if tr.TLSHandshakeTimeout != TLSPhaseTimeout {
		t.Errorf("TLS phase unbounded: %v", tr.TLSHandshakeTimeout)
	}
	if tr.TLSHandshakeTimeout >= c.Timeout || DialPhaseTimeout >= c.Timeout {
		t.Errorf("a phase deadline at or past the overall one never fires first: dial=%v tls=%v overall=%v",
			DialPhaseTimeout, tr.TLSHandshakeTimeout, c.Timeout)
	}
	if !tr.DisableKeepAlives {
		t.Error("a pooled connection presents the censor with nothing to inspect")
	}
}

func TestSandboxLocalAddrUsesEgressAddress(t *testing.T) {
	addr, err := sandboxLocalAddr(func() string { return "lo" }, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("local address is %T", addr)
	}
	if !tcp.IP.IsLoopback() {
		t.Fatalf("local address = %v, want loopback", tcp.IP)
	}
}

// The live canary's client must classify a failure the same way the lane's does,
// or the two graders describe one path in two languages. This half runs on every
// platform; the sandbox half needs a source address on the egress interface.
func TestTheBurstClientBoundsEachPhaseToo(t *testing.T) {
	tr, ok := BurstClient("", 20*time.Second).Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", BurstClient("", time.Second).Transport)
	}
	if tr.TLSHandshakeTimeout != TLSPhaseTimeout {
		t.Errorf("TLS phase unbounded: %v", tr.TLSHandshakeTimeout)
	}
	if tr.DialContext == nil {
		t.Error("the dial phase has no deadline of its own, so a dead SYN reports as a header timeout")
	}
}
