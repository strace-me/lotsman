package core

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
)

func TestNextFailoverProviderWraps(t *testing.T) {
	list := []string{"cloudflare", "quad9", "google"}
	cases := []struct{ cur, want string }{
		{"cloudflare", "quad9"},
		{"quad9", "google"},
		{"google", "cloudflare"},  // wrap
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
	d := &configv2.DNS{
		Servers: []configv2.DNSServer{
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
// A reload that does not apply must leave the config on the provider sing-box is
// actually running: otherwise memory walks the whole failover list while the box keeps
// the first one, and the loop then reports "every provider failed" about providers it
// never installed.
func TestRotateDNSFailoverCanBePutBackAfterAFailedApply(t *testing.T) {
	c := &Core{conf: dnsFailoverConf(t)}

	from, to, _, err := c.rotateDNSFailover()
	if err != nil || from != "cloudflare" || to != "quad9" {
		t.Fatalf("rotate: %q->%q err=%v", from, to, err)
	}
	// The apply failed — put it back.
	if _, _, _, err := c.rotateDNSFailoverTo(from); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := currentFinalProvider(c.conf.DNS); got != "cloudflare" {
		t.Errorf("after a failed apply the config must read %q again, got %q", "cloudflare", got)
	}
	var final configv2.DNSServer
	for _, s := range c.conf.DNS.Servers {
		if s.Name == c.conf.DNS.Final {
			final = s
		}
	}
	if final.Address != "1.1.1.1" {
		t.Errorf("the endpoint must be back on cloudflare's, got %q", final.Address)
	}
}

// LOT-83: the Direct resolver may be a manual pinned endpoint (an office DNS).
// The rotation must REPLACE it with a provider — endpoint and transport together.
func TestRotateDNSDirectFailoverReplacesManualDirect(t *testing.T) {
	d, err := configv2.BuildDNS(&configv2.DNSSection{
		Servers: []configv2.DNSSectionServer{
			{Name: "office", Type: "udp", Address: "192.168.10.30", Detour: "direct"},
			{Name: "remote", Provider: "cloudflare", Method: "tls", Detour: "vpn"},
		},
		Final: "remote", Direct: "office", DirectFailover: []string{"cloudflare", "quad9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	conf := &config.Config{DNS: d}
	c := &Core{conf: conf}
	if got := currentDirectProvider(c.conf.DNS); got != "" {
		t.Fatalf("a manual direct has no provider, got %q", got)
	}
	from, to, got, err := c.rotateDNSDirectFailover()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if from != "" || to != "cloudflare" || got == nil {
		t.Fatalf("first rotation should be manual->cloudflare, got %q->%q (conf nil=%v)", from, to, got == nil)
	}
	var direct configv2.DNSServer
	for _, s := range c.conf.DNS.Servers {
		if s.Name == "office" {
			direct = s
		}
	}
	if direct.Provider != "cloudflare" || direct.Address != "1.1.1.1" || direct.Type != "https" {
		t.Errorf("manual office DNS not replaced by cloudflare DoH: %+v", direct)
	}
	// A failed apply must put the manual endpoint back.
	if _, _, _, err := c.rotateDNSDirectFailoverTo(""); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

func TestCurrentDirectProviderReadsTheDirectServer(t *testing.T) {
	d := &configv2.DNS{
		Servers: []configv2.DNSServer{
			{Name: "direct-pub", Provider: "quad9"},
			{Name: "remote", Provider: "cloudflare"},
		},
		Direct: "direct-pub", Final: "remote",
	}
	if got := currentDirectProvider(d); got != "quad9" {
		t.Errorf("currentDirectProvider = %q, want quad9", got)
	}
}

func dnsFailoverConf(t *testing.T) *config.Config {
	t.Helper()
	d, err := configv2.BuildDNS(&configv2.DNSSection{
		Servers: []configv2.DNSSectionServer{
			{Name: "remote", Provider: "cloudflare", Method: "tls", Detour: "vpn"},
			{Name: "lan", Type: "local"},
		},
		Final: "remote", Direct: "lan", Failover: []string{"cloudflare", "quad9", "mullvad"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &config.Config{DNS: d}
}

func TestRotateDNSFailoverAdvancesAndRepointsEndpoint(t *testing.T) {
	c := &Core{conf: dnsFailoverConf(t)}

	from, to, got, err := c.rotateDNSFailover()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if from != "cloudflare" || to != "quad9" || got == nil {
		t.Fatalf("first rotation should be cloudflare->quad9, got %q->%q (conf nil=%v)", from, to, got == nil)
	}
	// The Final server's endpoint must now be quad9's.
	var final configv2.DNSServer
	for _, s := range c.conf.DNS.Servers {
		if s.Name == "remote" {
			final = s
		}
	}
	if final.Provider != "quad9" || final.Address != "9.9.9.9" || final.Type != "tls" {
		t.Errorf("Final not re-pointed to quad9 keeping transport: %+v", final)
	}
}
