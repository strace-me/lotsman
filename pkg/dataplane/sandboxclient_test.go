package dataplane

import (
	"net/http"
	"testing"
	"time"
)

// The two phases must be bounded separately, or a failure reports itself as
// "Client.Timeout exceeded while awaiting headers" — true, and useless for the
// only question that matters when nothing was delivered: was the block below TLS,
// where no desync recipe can help, or was it the hello being swallowed, which is
// what a recipe rewrites.
func TestTheSandboxClientBoundsEachPhaseSeparately(t *testing.T) {
	c := SandboxClient(0x4554, func() string { return "lo" }, 20*time.Second)
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

// The live canary's client must classify a failure the same way the lane's does,
// or the two graders describe one path in two languages. This half runs on every
// platform; the sandbox half needs SO_BINDTODEVICE.
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
