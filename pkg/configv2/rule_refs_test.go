package configv2

import "testing"

const refsConfig = `
desync:
  instances:
    nfqws1-main:
      engine: nfqws1
      enabled: true
    nfqws1-off:
      engine: nfqws1
      enabled: false
pools:
  vpn_a:
    type: urltest
lists:
  domains:
    example:
      domains: [a.example]
  ips:
    example:
      cidrs: ["10.0.0.0/24"]
`

func ruleWith(t *testing.T, body string) Rule {
	t.Helper()
	rule, err := ParseRule([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return *rule
}

func TestValidateReferencesAcceptsResolvableRule(t *testing.T) {
	cfg, err := Parse([]byte(refsConfig))
	if err != nil {
		t.Fatal(err)
	}
	rule := ruleWith(t, `
id: example
match:
  domain_lists: [example]
  ip_lists: [example]
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: alt12}}
  - {tier: fallback, vpn: {pool: vpn_a}}
`)
	if err := rule.ValidateReferences(cfg, map[string][]string{"nfqws1-main": {"alt12", "alt4"}}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReferencesRejectsUnknownNames(t *testing.T) {
	cfg, err := Parse([]byte(refsConfig))
	if err != nil {
		t.Fatal(err)
	}
	strategies := map[string][]string{"nfqws1-main": {"alt12"}}
	for name, body := range map[string]string{
		"unknown list": `
id: example
match:
  domain_lists: [missing]
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: alt12}}
`,
		"unknown pool": `
id: example
match:
  domains: [a.example]
chain:
  - {tier: fallback, vpn: {pool: missing}}
`,
		"unknown instance": `
id: example
match:
  domains: [a.example]
chain:
  - {tier: preferred, desync: {instance: missing, strategy: alt12}}
`,
		"unknown strategy": `
id: example
match:
  domains: [a.example]
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: alt99}}
`,
		"disabled instance": `
id: example
match:
  domains: [a.example]
chain:
  - {tier: preferred, desync: {instance: nfqws1-off, strategy: alt12}}
`,
		"empty match": `
id: example
chain:
  - {tier: preferred, direct: true}
`,
	} {
		rule := ruleWith(t, body)
		if err := rule.ValidateReferences(cfg, strategies); err == nil {
			t.Errorf("%s: broken reference was accepted", name)
		}
	}
}

func TestDevicesAcceptARealConsoleAndRefuseADanglingPool(t *testing.T) {
	// The owner runs a console that will not work on a Russian ISP and sends it
	// through the tunnel; that is the whole reason this block exists. It is an
	// EGRESS override, not a way to pick per-flow treatment, which the treatment
	// matrix forbids and which rules already do.
	cfg := &Configuration{Pools: map[string]Pool{"vpn_url_test": {Type: "url_test"}}}
	cfg.Devices = []Device{{Name: "ps5", Sources: []string{"192.168.1.50"}, Policy: "vpn_url_test"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a device pointing at a declared pool was refused: %v", err)
	}
	for _, keyword := range []string{"direct", "block"} {
		cfg.Devices = []Device{{Name: "ps5", Sources: []string{"192.168.1.50"}, Policy: keyword}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("policy %q was refused: %v", keyword, err)
		}
	}

	// A device naming a pool nobody declared is a load error, NOT a device that
	// quietly gets no override: the symptom would be "the PS5 stopped going
	// through VPN" with nothing in the log to explain it.
	cfg.Devices = []Device{{Name: "ps5", Sources: []string{"192.168.1.50"}, Policy: "typo_pool"}}
	if err := cfg.Validate(); err == nil {
		t.Error("a device naming an undeclared pool was accepted")
	}
	// No sources means no client matches, and a device that matches nothing is
	// indistinguishable from one that was never written.
	cfg.Devices = []Device{{Name: "ps5", Policy: "direct"}}
	if err := cfg.Validate(); err == nil {
		t.Error("a device with no sources was accepted")
	}
}

// A partial v2 file that names a BUILT-IN pool must load. FromV2 overlays the
// document on top of pools.Builtin() rather than replacing it, so vpn_url_test
// and emergency_pool resolve at run time — and a rule naming one is exactly what
// a partial file should be able to say. Validating against the document alone made
// a working rule fail to load.
func TestBuiltinPoolsCountAsDeclared(t *testing.T) {
	cfg := &Configuration{Pools: map[string]Pool{"vpn_bulk": {Type: "url_test"}}}
	rule := &Rule{ID: "youtube", Match: Match{Domains: []string{"googlevideo.com"}}, Chain: []ChainStep{
		{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "vpn_bulk"}}},
		{Tier: "fallback", Action: Action{VPN: &VPNAction{Pool: "vpn_url_test_udp"}}},
		{Tier: "emergency", Action: Action{VPN: &VPNAction{Pool: "emergency_pool"}}},
	}}
	if err := rule.ValidateReferences(cfg, map[string][]string{}); err != nil {
		t.Fatalf("a rule naming built-in pools was rejected: %v", err)
	}
	// A genuinely unknown pool must still fail, or the check is now vacuous.
	rule.Chain[1].Action.VPN = &VPNAction{Pool: "typo_pool"}
	if err := rule.ValidateReferences(cfg, map[string][]string{}); err == nil {
		t.Error("a rule naming an unknown pool was accepted")
	}
}
