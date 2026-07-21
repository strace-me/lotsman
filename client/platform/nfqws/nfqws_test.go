package nfqws

import "testing"

func TestParseDefaultRouteIface(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "ethernet",
			out:  "default via 192.168.1.1 dev eth0 proto dhcp src 192.168.1.50 metric 100\n",
			want: "eth0",
		},
		{
			name: "wifi with trailing newline",
			out:  "default via 10.0.0.1 dev wlan0 proto dhcp metric 600\n\n",
			want: "wlan0",
		},
		{
			// A laptop often has both; the first (lowest metric) line wins, which is
			// the route packets actually take.
			name: "multiple defaults picks the first",
			out: "default via 10.0.0.1 dev wlan0 proto dhcp metric 600\n" +
				"default via 192.168.1.1 dev eth0 proto dhcp metric 100\n",
			want: "wlan0",
		},
		{
			name: "no default route",
			out:  "10.0.0.0/24 dev wlan0 proto kernel scope link src 10.0.0.5\n",
			want: "",
		},
		{name: "empty", out: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseDefaultRouteIface(tc.out); got != tc.want {
				t.Errorf("ParseDefaultRouteIface() = %q, want %q", got, tc.want)
			}
		})
	}
}
