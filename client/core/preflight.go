package core

import (
	"fmt"
	"net"
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
