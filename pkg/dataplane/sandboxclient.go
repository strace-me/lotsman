package dataplane

import (
	"net"
	"net/http"
	"syscall"
	"time"
)

// SandboxClient returns an HTTP client whose sockets carry BOTH the sandbox
// fwmark and a binding to the physical interface.
//
// Each half answers a different question and neither is sufficient alone:
//
//   - The MARK is what makes the packet the sandbox's. The production nft table
//     returns marked packets untouched and the sandbox table queues only them, so
//     a marked probe is desynced exactly once, by the CANDIDATE, and the
//     household's traffic never meets it.
//   - The BINDING is what lets the packet leave at all. Under our own tun,
//     auto_route pulls every destination into the tunnel, so an unbound socket —
//     marked or not — is routed by the service's rule into whatever VPN node it
//     sits on. The mark would then be carried through a tunnel that no nft rule on
//     this box ever sees, and the "candidate" measurement would describe the VPN.
//
// Together they make the probe leave on the WAN, meet the sandbox queue, and
// measure the candidate strategy against the live DPI — which is the only way to
// ask "would this recipe work?" without first imposing it on real traffic.
//
// Returns nil where the binding is impossible (non-Linux): a client that silently
// dropped the binding would measure the tunnel and call it the candidate.
func SandboxClient(mark int, iface func() string, timeout time.Duration) *http.Client {
	ctrl := sandboxControl(mark, iface)
	if ctrl == nil {
		return nil
	}
	d := &net.Dialer{Timeout: DialPhaseTimeout, Control: ctrl}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: d.DialContext,
			// Establishing the connection is the thing being measured; a pooled one
			// would present the DPI with nothing and the candidate with no work to do.
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: TLSPhaseTimeout,
		},
	}
}

// DialPhaseTimeout and TLSPhaseTimeout bound the two phases SEPARATELY, and well
// inside the client's overall timeout, so a failure names itself.
//
// With one deadline covering everything, Go reports whichever fired first as
// "Client.Timeout exceeded while awaiting headers" — which is true and useless.
// The two phases mean opposite things here: a TCP handshake that never completes
// says the block is below TLS and no desync recipe can matter, because there will
// be no ClientHello to rewrite; a TLS handshake that dies on an established
// connection is the censor swallowing the hello, which is exactly what a recipe
// rewrites. Split, the transport says `dial tcp …: i/o timeout` for the first and
// `net/http: TLS handshake timeout` for the second, and the verdict carries the
// distinction for free.
//
// Both are generous for a working path and short next to the 20s the caller
// allows for pulling volume: a SYN that has gone unanswered for six seconds on a
// household uplink is not slow, it is blocked.
const (
	DialPhaseTimeout = 6 * time.Second
	TLSPhaseTimeout  = 8 * time.Second
)

// sandboxControl is the socket stamp both sandbox clients share: the mark that
// makes the packet the lane's, then the binding that lets it leave on the WAN.
// Shared rather than written twice, because the TCP and QUIC halves of a
// measurement differing in isolation would be indistinguishable from the network
// differing — the trap that cost a day when four HTTP clients turned out to
// differ in address family rather than in TLS shape.
//
// nil where the binding is impossible (non-Linux).
func sandboxControl(mark int, iface func() string) func(network, address string, c syscall.RawConn) error {
	bind := bindToDeviceControl(iface)
	if bind == nil {
		return nil
	}
	markc := markControl(mark)
	return func(network, address string, c syscall.RawConn) error {
		if err := bind(network, address, c); err != nil {
			return err
		}
		if markc == nil {
			return nil
		}
		return markc(network, address, c)
	}
}
