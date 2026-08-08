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
	d := &net.Dialer{Timeout: timeout, Control: ctrl}
	return &http.Client{
		Timeout: timeout,
		// Establishing the connection is the thing being measured; a pooled one
		// would present the DPI with nothing and the candidate with no work to do.
		Transport: &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true},
	}
}

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
