package netid

import "testing"

func TestParseDefaultRoute(t *testing.T) {
	cases := []struct {
		name, in, wantGW, wantIf string
	}{
		{
			name:   "typical wifi",
			in:     "default via 192.168.1.1 dev wlp0s20f3 proto dhcp src 192.168.1.85 metric 600",
			wantGW: "192.168.1.1", wantIf: "wlp0s20f3",
		},
		{
			name: "lowest metric wins over a second default",
			in: "default via 10.0.0.1 dev eth0 metric 100\n" +
				"default via 192.168.1.1 dev wlan0 metric 600",
			wantGW: "10.0.0.1", wantIf: "eth0",
		},
		{
			name:   "no default route",
			in:     "10.0.0.0/24 dev eth0 proto kernel scope link src 10.0.0.5",
			wantGW: "", wantIf: "",
		},
		{
			name:   "dev before via still parses",
			in:     "default dev tun0 via 10.9.0.1 scope link",
			wantGW: "10.9.0.1", wantIf: "tun0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gw, iface := parseDefaultRoute(c.in)
			if gw != c.wantGW || iface != c.wantIf {
				t.Errorf("parseDefaultRoute = %q,%q; want %q,%q", gw, iface, c.wantGW, c.wantIf)
			}
		})
	}
}

func TestParseNeighMAC(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"reachable", "192.168.1.1 dev wlp0s20f3 lladdr 3c:37:86:aa:bb:cc REACHABLE", "3c:37:86:aa:bb:cc"},
		{"stale", "192.168.1.1 dev eth0 lladdr 00:11:22:33:44:55 STALE", "00:11:22:33:44:55"},
		{"incomplete has no lladdr", "192.168.1.1 dev eth0  INCOMPLETE", ""},
		{"failed has no lladdr", "192.168.1.1 dev eth0 FAILED", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseNeighMAC(c.in); got != c.want {
				t.Errorf("parseNeighMAC(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

// The identity must be stable (same inputs -> same id) and case-insensitive on
// the MAC (ip may print either case), or returning to a network would miss its
// own store.
func TestShortHashStableAndShort(t *testing.T) {
	a := shortHash("mac:3c:37:86:aa:bb:cc")
	b := shortHash("mac:3c:37:86:aa:bb:cc")
	if a != b {
		t.Error("hash is not stable")
	}
	if len(a) != 12 {
		t.Errorf("id length = %d, want 12", len(a))
	}
	if shortHash("mac:aa") == shortHash("route:aa") {
		t.Error("different sources must not collide")
	}
}
