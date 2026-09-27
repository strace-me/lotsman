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
