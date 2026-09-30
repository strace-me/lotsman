package configv2

import "testing"

func TestChainAcceptsRepeatedTiers(t *testing.T) {
	// The real working config has four of eight services with two rungs in one
	// tier: a desync that names a pinned strategy and then one that defers to the
	// KB, or a UDP-native pool before a TCP one. An earlier "one action per tier"
	// rule rejected half of them.
	rule, err := ParseRule([]byte(`
id: youtube
match:
  domain_lists: [youtube]
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: alt12}}
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: auto}}
  - {tier: fallback, vpn: {pool: vpn_url_test_udp}}
  - {tier: fallback, vpn: {pool: vpn_url_test}}
  - {tier: emergency, vpn: {pool: emergency_pool}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rule.Chain) != 5 {
		t.Fatalf("chain length = %d", len(rule.Chain))
	}
	if rule.Chain[1].Action.Desync.Strategy != "auto" {
		t.Errorf("the KB-deferring rung lost its strategy: %#v", rule.Chain[1].Action.Desync)
	}
}

func TestChainNumberOfStagesIsUnbounded(t *testing.T) {
	body := "id: long\nmatch:\n  domains: [a.example]\nchain:\n"
	for i := 0; i < 12; i++ {
		body += "  - {tier: preferred, direct: true}\n"
	}
	rule, err := ParseRule([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(rule.Chain) != 12 {
		t.Fatalf("chain length = %d, want 12", len(rule.Chain))
	}
}

func TestAutoStrategyIsNotLookedUpInTheStore(t *testing.T) {
	cfg, err := Parse([]byte(`
desync:
  instances:
    nfqws1-main: {engine: nfqws1, enabled: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	rule, err := ParseRule([]byte(`
id: example
match:
  domains: [a.example]
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: auto}}
`))
	if err != nil {
		t.Fatal(err)
	}
	// The store holds only alt12; "auto" must still resolve, because it names the
	// knowledge base rather than a bundle.
	if err := rule.ValidateReferences(cfg, map[string][]string{"nfqws1-main": {"alt12"}}); err != nil {
		t.Fatal(err)
	}
	rule.Chain[0].Action.Desync.Strategy = "alt99"
	if err := rule.ValidateReferences(cfg, map[string][]string{"nfqws1-main": {"alt12"}}); err == nil {
		t.Fatal("a genuinely unknown strategy was accepted")
	}
}

func TestChainRejectsGoingBackwards(t *testing.T) {
	_, err := ParseRule([]byte(`
id: example
match:
  domains: [a.example]
chain:
  - {tier: emergency, vpn: {pool: emergency_pool}}
  - {tier: preferred, direct: true}
`))
	if err == nil {
		t.Fatal("a chain stepping back from emergency to preferred was accepted")
	}
}

func TestChainAcceptsUnlabelledSteps(t *testing.T) {
	// tier is a label for the UI, not a required field: a chain of plain actions
	// carries its order and nothing else.
	rule, err := ParseRule([]byte(`
id: example
match:
  domains: [a.example]
chain:
  - {desync: {instance: nfqws1-main, strategy: alt12}}
  - {vpn: {pool: vpn_a}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if rule.Chain[0].Tier != "" || rule.Chain[1].Tier != "" {
		t.Fatalf("an unlabelled step gained a tier: %#v", rule.Chain)
	}
}

func TestChainRejectsUnknownTier(t *testing.T) {
	if _, err := ParseRule([]byte("id: a\nchain:\n  - {tier: whenever, direct: true}\n")); err == nil {
		t.Fatal("an unknown tier was accepted")
	}
}

func TestChainRejectsTwoActionsInOneStep(t *testing.T) {
	if _, err := ParseRule([]byte("id: a\nchain:\n  - {tier: preferred, direct: true, vpn: {pool: p}}\n")); err == nil {
		t.Fatal("a step carrying two actions was accepted")
	}
}
