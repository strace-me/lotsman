package configv2

import "testing"

// Composition, not fallback. sing-box accepts a `detour` on a node outbound and it
// MAY name a group, so the underlay is itself a urltest that picks its own live
// member — measured on 1.14.1, and REFUSED on a urltest group, which is why this
// hangs on the nodes rather than on the pool.
func TestDetourIsAcceptedAndPointsAtAPool(t *testing.T) {
	cfg := &Configuration{Pools: map[string]Pool{"outer": {}, "underlay": {}}}
	rule := &Rule{ID: "a", Match: Match{Domains: []string{"x.example"}}, Chain: []ChainStep{
		{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "outer", Detour: "underlay"}}},
	}}
	if err := rule.ValidateReferences(cfg, map[string][]string{}); err != nil {
		t.Fatalf("a detour at a declared pool was refused: %v", err)
	}
}

// sing-box would ACCEPT a detour cycle and resolve nothing: the symptom is traffic
// that never connects while every status read says the strategy applied. It has to
// be refused at load, with the ring named, because "the detour does not work" is not
// something an operator can debug from a status page.
func TestDetourCyclesAreRefused(t *testing.T) {
	cfg := &Configuration{Pools: map[string]Pool{"a": {}, "b": {}, "c": {}}}
	mk := func(id, pool, detour string) Rule {
		return Rule{ID: id, Match: Match{Domains: []string{id + ".example"}}, Chain: []ChainStep{
			{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: pool, Detour: detour}}},
		}}
	}
	// A two-hop ring: a through b, b through a.
	rules := []Rule{mk("ra", "a", "b"), mk("rb", "b", "a")}
	if err := ValidateDetours(rules); err == nil {
		t.Error("a two-hop detour cycle was accepted")
	}
	// A three-hop ring, which a pairwise check would miss and this must not.
	rules = []Rule{mk("ra", "a", "b"), mk("rb", "b", "c"), mk("rc", "c", "a")}
	if err := ValidateDetours(rules); err == nil {
		t.Error("a three-hop detour cycle was accepted")
	}
	// A chain that terminates is composition, not a cycle.
	rules = []Rule{mk("ra", "a", "b"), mk("rb", "b", "c")}
	if err := ValidateDetours(rules); err != nil {
		t.Errorf("a detour chain that ends was refused: %v", err)
	}
	// Self-detour is the degenerate ring.
	rule := mk("ra", "a", "a")
	if err := rule.ValidateReferences(cfg, map[string][]string{}); err == nil {
		t.Error("a pool detouring through itself was accepted")
	}
}

// Two rules sending the SAME pool through different pools is not a cycle but is
// just as unresolvable: one pool has one path out.
func TestOnePoolCannotHaveTwoOutgoingDetours(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Chain: []ChainStep{{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "a", Detour: "b"}}}}},
		{ID: "r2", Chain: []ChainStep{{Tier: "preferred", Action: Action{VPN: &VPNAction{Pool: "a", Detour: "c"}}}}},
	}
	if err := ValidateDetours(rules); err == nil {
		t.Error("one pool detoured through two different pools was accepted")
	}
}
