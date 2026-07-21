package core

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/singbox"
)

func TestPortFreeDetectsAnOccupiedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = portFree("Clash-API", ln.Addr().String())
	if err == nil {
		t.Fatal("an occupied address must be reported, not accepted — otherwise the " +
			"client would authenticate against a foreign control plane and steer it")
	}
	// The operator has to be able to act on it, so the message must name both the
	// resource and the address.
	for _, want := range []string{"Clash-API", ln.Addr().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestPortFreeAcceptsAFreeAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // now free

	if err := portFree("proxy", addr); err != nil {
		t.Errorf("a free address must be accepted, got %v", err)
	}
}

func TestAddrInUseFindsALocalAddress(t *testing.T) {
	// Loopback always carries 127.0.0.1, so this is a stable positive case.
	iface, taken := addrInUse("127.0.0.1/8")
	if !taken {
		t.Fatal("127.0.0.1 must be reported as in use")
	}
	if iface == "" {
		t.Error("the owning interface must be named so the operator can find it")
	}
}

func TestAddrInUseIgnoresAnUnusedAddress(t *testing.T) {
	// TEST-NET-1: reserved for documentation, never configured on a host.
	if iface, taken := addrInUse("192.0.2.77/32"); taken {
		t.Errorf("192.0.2.77 must be free, reported on %q", iface)
	}
}

func TestAddrInUseToleratesGarbage(t *testing.T) {
	if _, taken := addrInUse("not-a-cidr"); taken {
		t.Error("an unparseable address must not be reported as in use")
	}
}

func TestAbsDirMakesPathsIndependentOfTheWorkingDirectory(t *testing.T) {
	// nfqws resolves relative paths against its own working directory. A relative
	// hostlist path works when the operator runs the client by hand from the right
	// folder and silently fails once systemd starts it elsewhere — the desync then
	// loads nothing and reports nothing.
	got := absDir("hostlists")
	if !filepath.IsAbs(got) {
		t.Errorf("absDir(%q) = %q, want an absolute path", "hostlists", got)
	}
	if absDir("") != "" {
		t.Error("an empty path means 'unset' and must stay empty")
	}
	if got := absDir("/already/absolute"); got != "/already/absolute" {
		t.Errorf("an absolute path must be left alone, got %q", got)
	}
}

func TestDefaultTunKeepsLocalDiscoveryOffTheTunnel(t *testing.T) {
	// Wiring, not generation: the generator honouring ExcludeRoutes means nothing
	// if the client never sets them. Capturing multicast breaks LocalSend, mDNS,
	// printer and cast discovery, and asymmetrically — this host still hears its
	// neighbours while none of them hear it, which reads as a firewall fault.
	tun := (&Core{}).tunOptions()
	if tun == nil {
		t.Fatal("a client with no explicit tun must still get a default")
	}
	want := map[string]bool{"224.0.0.0/4": false, "ff00::/8": false}
	for _, e := range tun.ExcludeRoutes {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for cidr, present := range want {
		if !present {
			t.Errorf("%s must be excluded from auto_route, got %v", cidr, tun.ExcludeRoutes)
		}
	}
}

func TestExplicitTunIsRespected(t *testing.T) {
	// An operator who supplies a tun owns it entirely; we must not silently graft
	// our defaults onto their choice.
	mine := &singbox.TunOptions{Address: []string{"10.9.0.1/30"}}
	c := &Core{opts: Options{Tun: mine}}
	if got := c.tunOptions(); got != mine {
		t.Errorf("tunOptions() = %+v, want the caller's own", got)
	}
}
