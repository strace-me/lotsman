package core

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/singbox"
)

func TestTunSentinelIsThePeerNotTheOwnAddress(t *testing.T) {
	// The host is pointed at the tun PEER (.2), not the tun's own address (.1), which
	// with stack:system is delivered locally and never hijacked.
	if got := tunSentinel(&singbox.TunOptions{Address: []string{"172.19.0.1/30"}}); got != "172.19.0.2" {
		t.Errorf("tunSentinel = %q, want the peer 172.19.0.2", got)
	}
	if got := tunSentinel(&singbox.TunOptions{}); got != "" {
		t.Errorf("tunSentinel with no address = %q, want empty", got)
	}
	if got := tunSentinel(nil); got != "" {
		t.Errorf("tunSentinel(nil) = %q, want empty", got)
	}
	if got := tunSentinel(&singbox.TunOptions{Address: []string{"garbage"}}); got != "" {
		t.Errorf("tunSentinel(garbage) = %q, want empty", got)
	}
}

func TestPinDirectResolverReplacesLocalWithAConcreteUDPServer(t *testing.T) {
	// The default split-DNS: dns_direct is type:local (reads resolv.conf). Under a
	// host-DNS redirect that would loop, so it must become a concrete UDP resolver.
	dns := defaultDNS("vpn_url_test")
	pinDirectResolver(dns, "192.168.1.1")

	var direct *singbox.DNSServer
	for i := range dns.Servers {
		if dns.Servers[i].Tag == dns.Direct {
			direct = &dns.Servers[i]
		}
	}
	if direct == nil {
		t.Fatal("no direct server after pin")
	}
	if direct.Type != "udp" || direct.Server != "192.168.1.1" {
		t.Errorf("pinned direct = %+v, want {type:udp server:192.168.1.1}", *direct)
	}
	// The remote (DoH) server must be untouched — only the direct one is pinned.
	for _, s := range dns.Servers {
		if s.Tag == dns.Final && s.Type != "https" {
			t.Errorf("pin must not touch the remote server, got %+v", s)
		}
	}
}

func TestPinDirectResolverIsANoOpWhenNothingToPin(t *testing.T) {
	dns := &singbox.DNSOptions{
		Servers: []singbox.DNSServer{{Tag: "dns_remote", Type: "https"}},
		Direct:  "dns_direct", // names a server that isn't present
		Final:   "dns_remote",
	}
	pinDirectResolver(dns, "192.168.1.1") // must not panic or invent a server
	if len(dns.Servers) != 1 {
		t.Errorf("pin invented a server: %+v", dns.Servers)
	}
	// An empty resolver or empty Direct is also a clean no-op.
	pinDirectResolver(defaultDNS("vpn_url_test"), "")
}
