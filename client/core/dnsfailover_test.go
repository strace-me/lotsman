package core

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

func TestNextFailoverProviderWraps(t *testing.T) {
	list := []string{"cloudflare", "quad9", "google"}
	cases := []struct{ cur, want string }{
		{"cloudflare", "quad9"},
		{"quad9", "google"},
		{"google", "cloudflare"}, // wrap
		{"mullvad", "cloudflare"}, // not in list => start at front
		{"", "cloudflare"},        // unset => start at front
	}
	for _, c := range cases {
		if got := nextFailoverProvider(c.cur, list); got != c.want {
			t.Errorf("nextFailoverProvider(%q) = %q, want %q", c.cur, got, c.want)
		}
	}
	if got := nextFailoverProvider("cloudflare", nil); got != "" {
		t.Errorf("empty list => %q, want empty", got)
	}
}

func TestCurrentFinalProviderReadsTheFinalServer(t *testing.T) {
	d := &config.DNS{
		Servers: []config.DNSServer{
			{Name: "remote", Provider: "quad9"},
			{Name: "lan", Provider: ""},
		},
		Final: "remote",
	}
	if got := currentFinalProvider(d); got != "quad9" {
		t.Errorf("currentFinalProvider = %q, want quad9", got)
	}
	d.Final = "lan"
	if got := currentFinalProvider(d); got != "" {
		t.Errorf("a manual final has no provider, got %q", got)
	}
}

// TestRotateDNSFailoverAdvancesAndRepointsEndpoint drives the Core-level rotation the
// loop uses: it should move Final to the next provider and re-point the endpoint.
func TestRotateDNSFailoverAdvancesAndRepointsEndpoint(t *testing.T) {
	d, err := config.ParseDocument([]byte(`
subscriptions:
  - { name: s, url: "https://e/x", format: auto, enabled: true }
services:
  - { name: yt, category: streaming, probe_target: https://x }
dns:
  servers:
    - { name: remote, provider: cloudflare, method: tls, detour: vpn }
    - { name: lan, type: local }
  final: remote
  direct: lan
  failover: [cloudflare, quad9, mullvad]
`))
	if err != nil {
		t.Fatal(err)
	}
	y, _ := d.YAML()
	conf, err := config.Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{conf: conf}

	from, to, got, err := c.rotateDNSFailover()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if from != "cloudflare" || to != "quad9" || got == nil {
		t.Fatalf("first rotation should be cloudflare->quad9, got %q->%q (conf nil=%v)", from, to, got == nil)
	}
	// The Final server's endpoint must now be quad9's.
	var final config.DNSServer
	for _, s := range c.conf.DNS.Servers {
		if s.Name == "remote" {
			final = s
		}
	}
	if final.Provider != "quad9" || final.Address != "9.9.9.9" || final.Type != "tls" {
		t.Errorf("Final not re-pointed to quad9 keeping transport: %+v", final)
	}
}
