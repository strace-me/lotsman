//go:build linux

package dataplane

import "syscall"

// bindToDeviceControl returns a net.Dialer.Control hook that binds the socket to
// a named interface before connect.
//
// This is how a probe escapes our own tun. sing-box's auto_route installs ip
// rules that pull every destination into the tunnel, so a plain dial from this
// process is captured and follows the service's route rule — which, while the
// service sits on a VPN node, means the probe measures the tunnel and credits the
// desync rung with its health (LOT-44, and the defect that dragged YouTube back
// onto a rung nobody had measured). SO_BINDTODEVICE bypasses the routing table
// lookup entirely, so the packet leaves on the physical NIC, where the production
// nft rule (`oifname <wan>`) queues it to nfqws — which is exactly the path the
// rung under test uses.
//
// Needs CAP_NET_RAW. The client runs as root; off Linux this file is not built
// and the caller falls back to refusing the measurement rather than inventing it.
func bindToDeviceControl(iface func() string) func(network, address string, c syscall.RawConn) error {
	if iface == nil {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		// Resolved per DIAL, not captured once. The interface changes on a roam, and
		// a prober built at startup would otherwise keep binding to the network the
		// laptop was on when it booted — the same snapshot hole that made a reconcile
		// re-assert a previous network's tun excludes.
		name := iface()
		if name == "" {
			return nil // nothing to bind to; the caller's guard decides what that means
		}
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, name)
		}); err != nil {
			return err
		}
		return serr
	}
}
