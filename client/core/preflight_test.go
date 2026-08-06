package core

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/strategy"
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

// The IPv6 notice must track the CONDITION, not the startup. On the ThinkPad the
// same binary warned at 21:03 and was correctly silent at 21:39, because IPv6 had
// been turned off in between — a notice latched at startup would have gone on
// accusing a machine that had already fixed itself.
func TestIPv6NoticeAppearsOnlyWhenItIsTrue(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{
		"ai": {Name: "ai", Chain: []registry.ChainStep{{StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"}}},
		"yt": {Name: "yt", Chain: []registry.ChainStep{{StrategyClass: strategy.ClassZapret}}},
	}}
	cases := []struct {
		name    string
		opts    Options
		hostV6  bool
		wantOut bool
	}{
		{"tun, host has v6, not captured", Options{}, true, true},
		{"tun, host has no v6", Options{}, false, false},
		{"v6 captured", Options{TunIPv6: true}, true, false},
		{"proxy mode claims no tun", Options{ProxyListen: "127.0.0.1:1080"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Core{opts: tc.opts, reg: reg}
			n := c.ipv6EscapeNoticeFor(tc.hostV6)
			if (n != nil) != tc.wantOut {
				t.Fatalf("notice present = %v, want %v", n != nil, tc.wantOut)
			}
			if n == nil {
				return
			}
			// Only tunnel-intended services are named: a zapret-preferred service
			// routes direct BY DESIGN, so listing it would be the same
			// verdict-about-something-unmeasured this project keeps finding.
			if len(n.Services) != 1 || n.Services[0] != "ai" {
				t.Errorf("affected services = %v, want [ai]", n.Services)
			}
			if n.Code != NoticeIPv6Escape {
				t.Errorf("code = %q", n.Code)
			}
		})
	}
}

// A Core with no config yet must still answer, because /status does.
func TestIPv6NoticeSurvivesAnUnconfiguredCore(t *testing.T) {
	c := &Core{}
	if n := c.ipv6EscapeNoticeFor(true); n == nil || len(n.Services) != 0 {
		t.Errorf("want a notice with no services on a core with no registry, got %+v", n)
	}
}
