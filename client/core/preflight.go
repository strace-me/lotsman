package core

import (
	"fmt"
	"net"
	"sort"
)

// preflight refuses to start when something already owns the resources this
// client is about to claim.
//
// The failure this guards against is not the noisy one. If another sing-box (or
// any VPN client) already serves a Clash API on our port and does not require a
// secret, our control plane authenticates against IT and starts steering a
// stranger's tunnel — silently, while our own box fails to bind. A stale or
// foreign tun carrying the address we would assign conflicts just as quietly.
// Both are cheap to detect up front and confusing to diagnose afterwards.
func (c *Core) preflight() error {
	if err := portFree("Clash-API", c.opts.ClashListen); err != nil {
		return err
	}
	if c.opts.ProxyListen != "" {
		if err := portFree("proxy", c.opts.ProxyListen); err != nil {
			return err
		}
		return nil // proxy mode claims no tun
	}
	for _, cidr := range c.tunOptions().Address {
		if iface, taken := addrInUse(cidr); taken {
			return fmt.Errorf("core: tun address %s is already on interface %q — "+
				"another tunnel is running; stop it or choose a different address", cidr, iface)
		}
	}
	return nil
}

// portFree reports whether addr can be bound right now. The listener is released
// immediately; sing-box binds a moment later, and losing that race to a third
// party is far less likely than the steady-state collision this catches.
func portFree(what, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("core: the %s address %s is already in use (%v) — another "+
			"sing-box or VPN client is probably running; point this one at a free port",
			what, addr, err)
	}
	return ln.Close()
}

// addrInUse returns the interface already carrying cidr's address, if any.
func addrInUse(cidr string) (string, bool) {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return ifc.Name, true
			}
		}
	}
	return "", false
}

// warnIPv6Escape says so when the tunnel is about to protect IPv4 only while the
// machine has working IPv6.
//
// auto_route captures the address families the tun carries an address for, so a
// v4-only tun leaves the v6 default route on the physical link. Every VPN-rung
// service whose name resolves to AAAA then egresses DIRECT — and the desync rung
// does not cover it either, because a desync is not an exit. A service the operator
// deliberately made VPN-only is the one this hurts most, since VPN-only is exactly
// the claim it breaks.
//
// This warns rather than refuses. Capturing v6 when the exit nodes cannot carry it
// trades a silent leak for dead connections, and which of those is worse is the
// operator's call, not ours (LOT-22 family: report the leak, do not pick for them).
func (c *Core) warnIPv6Escape() {
	if c.opts.ProxyListen != "" || c.opts.TunIPv6 {
		return // no tun to escape, or v6 is captured
	}
	if !hostHasGlobalIPv6() {
		return // nothing to leak
	}
	var vpnOnly []string
	for name, svc := range c.reg.Services {
		if svc.TunnelIntended() {
			vpnOnly = append(vpnOnly, name)
		}
	}
	sort.Strings(vpnOnly)
	c.log.Warn("IPv6 is NOT captured by the tunnel and this machine has a global IPv6 address — "+
		"any service whose name resolves to AAAA egresses direct, unprotected. Pass -tun-ipv6 to capture it "+
		"(needs exit nodes that can carry IPv6), or disable IPv6 on this host",
		"tunnel_intended_services", vpnOnly)
}

// hostHasGlobalIPv6 reports whether any interface carries a global-scope IPv6
// address — the condition under which an uncaptured v6 default route is a real
// escape rather than a theoretical one.
func hostHasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() != nil {
			continue
		}
		if n.IP.IsGlobalUnicast() && !n.IP.IsLinkLocalUnicast() && !n.IP.IsPrivate() {
			return true
		}
	}
	return false
}
