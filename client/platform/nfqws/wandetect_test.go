package nfqws

import "testing"

// Hanging the queue on a tunnel is a silent death: no user traffic leaves by that
// interface, nothing is queued, `flags bypass` passes it all through untouched,
// and every status downstream reads healthy.
//
// Today it does not bite only because sing-box's auto_route puts its default in a
// POLICY table while this reads the main one. That is luck. WireGuard with
// `Table = main`, OpenVPN with redirect-gateway, or a differently configured
// sing-box would each put a default here.
func TestDefaultRouteSkipsTunnels(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{
			"tunnel first, physical after",
			"default via 172.19.0.2 dev tun0 proto static\ndefault via 192.168.1.1 dev wlp0s20f3 proto dhcp metric 600",
			"wlp0s20f3",
		},
		{
			"only the physical one, as on a plain host",
			"default via 192.168.1.1 dev wlp0s20f3 proto dhcp metric 600",
			"wlp0s20f3",
		},
		{
			"wireguard in the main table",
			"default dev wg0 scope link\ndefault via 10.0.0.1 dev eth0",
			"eth0",
		},
		{
			// Refusing beats guessing: naming the tunnel would arm a queue no traffic
			// passes through, and the caller retries on the next reconcile.
			"nothing but tunnels",
			"default via 172.19.0.2 dev tun0\ndefault dev wg0 scope link",
			"",
		},
		{"a subnet route is not a default", "192.168.1.0/24 dev wlp0s20f3 proto kernel", ""},
	}
	for _, c := range cases {
		if got := ParseDefaultRouteIface(c.out); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
