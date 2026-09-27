package configv2

import "testing"

func sampleRule(id string, priority int, chain ...ChainStep) Rule {
	return Rule{
		ID: id, Name: id, Enabled: true, Priority: priority,
		Match: Match{Domains: []string{"a.example"}},
		Probe: &Probe{Type: "http", Target: "https://a.example/"},
		Chain: chain,
	}
}

func TestBuildRegistrySkipsDisabledRules(t *testing.T) {
	// v1 drops a disabled service out of buildRegistry entirely, so no consumer
	// needs a guard. If a v2 rule survived here, one forgotten guard would probe a
	// service the operator switched off.
	on := sampleRule("on", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	off := sampleRule("off", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	off.Enabled = false
	reg, err := BuildRegistry(nil, []Rule{on, off}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Services) != 1 {
		t.Fatalf("services = %#v", reg.Services)
	}
	if _, ok := reg.Services["off"]; ok {
		t.Error("a disabled rule reached the registry")
	}
}

func TestBuildRegistryNamesListsSoTheyCannotCollide(t *testing.T) {
	// A list name and a sing-box rule-set tag share one string field. Without a
	// namespace the two would be indistinguishable, and a collision would look
	// exactly like a real match.
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	rule.Match = Match{DomainLists: []string{"shared"}, IPLists: []string{"voices"}}
	reg, err := BuildRegistry(nil, []Rule{rule}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Services["a"]
	want := []string{"list:domain/shared", "list:ip/voices"}
	if len(got.RuleSets) != len(want) {
		t.Fatalf("RuleSets = %#v", got.RuleSets)
	}
	for i := range want {
		if got.RuleSets[i] != want[i] {
			t.Fatalf("RuleSets = %#v, want %#v", got.RuleSets, want)
		}
		kind, name, ok := ParseListTag(got.RuleSets[i])
		if !ok || name == "" || kind == "" {
			t.Errorf("tag %q does not round-trip: kind=%q name=%q ok=%v", got.RuleSets[i], kind, name, ok)
		}
	}
}

func TestBuildRegistryMapsTiersOntoStates(t *testing.T) {
	// The tier is the operator's grouping; the state is the position. Both the
	// status surface and the Brain's logs read the state, so the mapping has to keep
	// "the first rung is the preferred one" and "a second desync rung is still
	// desync" — which is what v1 spelled ALT_ZAPRET.
	rule := sampleRule("a", 0,
		ChainStep{Tier: "preferred", Action: Action{Desync: &DesyncAction{Instance: "i", Strategy: "alt12"}}},
		ChainStep{Tier: "preferred", Action: Action{Desync: &DesyncAction{Instance: "i", Strategy: "auto"}}},
		ChainStep{Tier: "fallback", Action: Action{VPN: &VPNAction{Pool: "udp"}}},
		ChainStep{Tier: "fallback", Action: Action{VPN: &VPNAction{Pool: "tcp"}}},
		ChainStep{Tier: "emergency", Action: Action{VPN: &VPNAction{Pool: "emergency_pool"}}},
	)
	reg, err := BuildRegistry(nil, []Rule{rule}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PREFERRED", "ALT_ZAPRET", "VPN", "VPN", "EMERGENCY"}
	chain := reg.Services["a"].Chain
	if len(chain) != len(want) {
		t.Fatalf("chain = %#v", chain)
	}
	for i, state := range want {
		if chain[i].State != state {
			t.Errorf("step %d state = %q, want %q (full: %#v)", i, chain[i].State, state, chain)
		}
	}
	if chain[0].StrategyID != "alt12" || chain[1].StrategyID != "auto" {
		t.Errorf("strategies did not carry: %#v", chain)
	}
}

func TestBuildRegistryRefusesBypass(t *testing.T) {
	// v1 has no bypass rung. The nearest thing is a switched-off service, and
	// carrying it as "direct" would assert something false: that traffic is routed
	// there on purpose. Better to refuse than to mistranslate.
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{Bypass: true}})
	if _, err := BuildRegistry(nil, []Rule{rule}, nil, nil); err == nil {
		t.Fatal("a bypass rung was silently translated")
	}
}

func TestBuildRegistryPicksTheFirstVolumeTargetAsPrimary(t *testing.T) {
	// An empty primary would quietly become the probe target — a 204 with no body.
	// That is how a throughput measurement once read 0 KiB/s and demoted a working
	// strategy, so the first named target must be the primary.
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{Direct: true}})
	rule.Volume = &Volume{Targets: []string{"https://big.example/x", "https://small.example/y"}, MinBytes: 65536}
	reg, err := BuildRegistry(nil, []Rule{rule}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := reg.Services["a"]
	if service.VolumeTarget != "https://big.example/x" {
		t.Errorf("primary target = %q", service.VolumeTarget)
	}
	if len(service.VolumeTargets) != 1 || service.VolumeTargets[0] != "https://small.example/y" {
		t.Errorf("extra targets = %#v", service.VolumeTargets)
	}
	if service.VolumeBytes != 65536 {
		t.Errorf("volume bytes = %d", service.VolumeBytes)
	}
}
