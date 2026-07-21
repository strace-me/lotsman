package core

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
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
