package configv2

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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

func TestListsResolveToRuleSetTagsTheGeneratorCanPlace(t *testing.T) {
	// The generator maps a rule-set tag to a PATH BY ITS PREFIX
	// (geosite-* -> rule-set-geosite/, geoip-* -> rule-set-geoip/) and skips
	// anything it does not recognise. A namespaced name like "list:domain/x"
	// therefore produces a routing rule pointing at a rule-set nothing ever
	// defines: the config passes `sing-box check`, because sing-box resolves
	// rule-sets lazily on first match, and matches NOTHING at run time.
	//
	// Comparing a v1-generated config against a v2-generated one byte for byte is
	// what caught this. No unit test would have.
	cfg := &Configuration{Lists: Lists{
		Domains: map[string]List{"shared": {Dataset: "geosite", Category: "shared"}},
		IPs:     map[string]List{"shared": {Dataset: "geoip", Category: "shared"}},
	}}
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	rule.Match = Match{DomainLists: []string{"shared"}, IPLists: []string{"shared"}}

	reg, err := BuildRegistry(cfg, []Rule{rule}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The SAME list name in both namespaces must land on two DIFFERENT files, or
	// the ip rule would be silently answered by the domain one.
	want := []string{"geosite-shared", "geoip-shared"}
	got := reg.Services["a"].RuleSets
	if len(got) != len(want) {
		t.Fatalf("RuleSets = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RuleSets = %#v, want %#v", got, want)
		}
	}
}

// A domain list used where an IP list is required is a rule that can never fire,
// not a partial match, so it is a load error rather than a silent mis-route.
func TestListOfTheWrongKindIsRefused(t *testing.T) {
	cfg := &Configuration{Lists: Lists{Domains: map[string]List{"only-domain": {Dataset: "geosite"}}}}
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	rule.Match = Match{IPLists: []string{"only-domain"}}
	if _, err := BuildRegistry(cfg, []Rule{rule}, nil, nil); err == nil {
		t.Fatal("a domain list was accepted in an ip_list position")
	}
	// And an undeclared list is an error, not an empty match.
	rule.Match = Match{DomainLists: []string{"typo"}}
	if _, err := BuildRegistry(cfg, []Rule{rule}, nil, nil); err == nil {
		t.Fatal("an undeclared list was accepted")
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

func TestBridgeCarriesBehaviourAcross(t *testing.T) {
	// The bridge is the seam that makes a v2 cutover a parser swap instead of a
	// rewrite, so a field it forgets is a field the operator sets and the runtime
	// never sees. Measured once already: the first bridge dropped all seven of
	// these. This is the guard against it happening quietly again.
	sticky, unpinned := true, false
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	rule.Behavior = Behavior{
		Sticky:        &sticky,
		Profile:       "voice",
		SpreadClients: []string{"192.168.1.0/24"},
		EscalateAfter: 3,
		RecoverAfter:  5,
		TLSFragment:   true,
	}
	reg, err := BuildRegistry(nil, []Rule{rule}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Services["a"]
	if !got.Sticky || got.Profile != "voice" || got.EscalateAfter != 3 || got.RecoverAfter != 5 || !got.TLSFragment {
		t.Errorf("behaviour did not survive the bridge: %+v", got)
	}
	if len(got.SpreadClients) != 1 || got.SpreadClients[0] != "192.168.1.0/24" {
		t.Errorf("spread clients = %#v", got.SpreadClients)
	}

	// Unset must not read as an opinion. A rule that says nothing about sticky
	// takes the global default, and a plain bool would have turned every silent
	// rule into one that asks to flap by latency.
	quiet := sampleRule("b", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	reg, err = BuildRegistry(nil, []Rule{quiet}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Services["b"].Sticky {
		t.Error("a rule that never mentioned sticky came out sticky")
	}

	// An explicit false is not the same as unset and must not be lost either.
	rule = quiet
	rule.Behavior.Sticky = &unpinned
	reg, err = BuildRegistry(nil, []Rule{rule}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Services["b"].Sticky {
		t.Error("an explicit sticky:false was dropped")
	}
}

func TestRuleRejectsAnUnknownTrafficClass(t *testing.T) {
	// A class the balancer does not know would be treated as general, and the
	// rule would quietly get the wrong node weighting — the enum table is the
	// single source of truth, so an unknown value must fail the load.
	rule := sampleRule("a", 0, ChainStep{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "p"}}})
	rule.Behavior.Profile = "cinema"
	rule.ID = "a"
	data, err := yaml.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	// ParseRule validates on the way in, which is the path a real load takes.
	if _, err := ParseRule(data); err == nil {
		t.Fatal("an unknown traffic class was accepted")
	}
}
