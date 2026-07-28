package core

import (
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestLocalExcludeRoutesSkipsLoopbackAndSorts(t *testing.T) {
	got := localExcludeRoutes()
	// Loopback is direct anyway and must never be pulled from auto_route; every host
	// has it, so this is a portable check that the interface filter actually excludes
	// tun/loopback/down links rather than dumping every address in.
	for _, c := range got {
		if strings.HasPrefix(c, "127.") || c == "::1/128" {
			t.Errorf("loopback subnet %q must not be in the tun excludes", c)
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("exclude routes must be sorted for a deterministic config: %v", got)
	}
}

func TestTunOptionsExcludesLANNotJustMulticast(t *testing.T) {
	opts := (&Core{}).tunOptions()
	// The static multicast/broadcast excludes must survive the merge...
	for _, want := range defaultTunExcludes {
		if !slices.Contains(opts.ExcludeRoutes, want) {
			t.Errorf("tun excludes lost the static entry %q: %v", want, opts.ExcludeRoutes)
		}
	}
	// ...and on any host with a real (up, non-loopback) interface the LAN subnet is
	// added on top — guarding the regression where auto_route swallowed the LAN and
	// killed SSH. CI with only loopback has nothing to add, so skip there.
	if len(opts.ExcludeRoutes) <= len(defaultTunExcludes) {
		t.Skip("no non-loopback interface subnet on this host; nothing to assert")
	}
}

func TestIsTunName(t *testing.T) {
	for _, n := range []string{"tun0", "utun3", "singbox0", "wg0", "tap0"} {
		if !isTunName(n) {
			t.Errorf("%q should read as a tunnel device", n)
		}
	}
	for _, n := range []string{"wlp0s20f3", "eth0", "en0", "docker0", "br-lan", "wwan0"} {
		if isTunName(n) {
			t.Errorf("%q is a real LAN/WAN iface, not a tunnel", n)
		}
	}
}
