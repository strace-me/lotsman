package zapret

import (
	"strings"
	"testing"
)

func TestValidateOK(t *testing.T) {
	in := []Instance{
		{Name: "tls", QNum: 200, Capture: Capture{TCP: []string{"80", "443"}, UDP: []string{"443"}}, Connbytes: 12},
		{Name: "voice", QNum: 201, Capture: Capture{UDP: []string{"19294-19344", "50000-50100"}}, Connbytes: 0},
	}
	if err := Validate(in); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateCatchesCollisions(t *testing.T) {
	cases := map[string][]Instance{
		"dup name": {
			{Name: "a", QNum: 200, Capture: Capture{TCP: []string{"443"}}},
			{Name: "a", QNum: 201, Capture: Capture{TCP: []string{"80"}}},
		},
		"dup qnum": {
			{Name: "a", QNum: 200, Capture: Capture{TCP: []string{"443"}}},
			{Name: "b", QNum: 200, Capture: Capture{TCP: []string{"80"}}},
		},
		"port overlap": {
			{Name: "a", QNum: 200, Capture: Capture{TCP: []string{"443"}}},
			{Name: "b", QNum: 201, Capture: Capture{TCP: []string{"443"}}}, // both want 443
		},
		"bad qnum": {
			{Name: "a", QNum: 0, Capture: Capture{TCP: []string{"443"}}},
		},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(in); err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

func TestVariantA_SingleInstance(t *testing.T) {
	// One instance capturing everything = the classic global nfqws (Variant A).
	in := []Instance{{
		Name: "all", QNum: 200,
		Capture:   Capture{TCP: []string{"80", "443"}, UDP: []string{"443", "50000-50100"}},
		Connbytes: 12,
	}}
	if err := Validate(in); err != nil {
		t.Fatal(err)
	}
	opts := DefaultNftOptions()
	opts.VPNServers = []string{"198.51.100.10"}
	nft := GenerateNft(in, opts)

	for _, want := range []string{
		"table inet zapret {",
		"ip daddr { 198.51.100.10 } return",
		`oifname "eth0" meta l4proto tcp tcp dport { 80, 443 } ct original packets 1-12 queue num 200 bypass`,
		`oifname "eth0" meta l4proto udp udp dport { 443, 50000-50100 } ct original packets 1-12 queue num 200 bypass`,
	} {
		if !strings.Contains(nft, want) {
			t.Errorf("nft missing line:\n  %s\n--- got ---\n%s", want, nft)
		}
	}
}

func TestVariantB_PerRuleConnbytes(t *testing.T) {
	// Two instances; voice uses connbytes=0 (whole stream, the anti-lag tuning).
	in := []Instance{
		{Name: "tls", QNum: 200, Capture: Capture{TCP: []string{"443"}}, Connbytes: 12},
		{Name: "voice", QNum: 201, Capture: Capture{UDP: []string{"50000-50100"}}, Connbytes: 0},
	}
	if err := Validate(in); err != nil {
		t.Fatal(err)
	}
	nft := GenerateNft(in, DefaultNftOptions())
	if !strings.Contains(nft, "queue num 200 bypass") || !strings.Contains(nft, "queue num 201 bypass") {
		t.Errorf("missing per-instance queues:\n%s", nft)
	}
	// voice (connbytes 0) must NOT carry a ct-packets limit; tls (12) must.
	if strings.Contains(nft, "50000-50100 } ct original") {
		t.Errorf("voice should have no connbytes limit:\n%s", nft)
	}
	if !strings.Contains(nft, `dport { 443 } ct original packets 1-12`) {
		t.Errorf("tls should have connbytes 12:\n%s", nft)
	}
}
