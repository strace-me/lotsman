package configv2

import (
	"reflect"
	"testing"
)

func TestBuildMetadata(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	metadata := BuildMetadata(cfg, nil, []string{"vpn_a", "vpn_b"}, []StrategyInfo{
		{Instance: "nfqws1-main", Engine: "nfqws1", Strategy: "alt12", Revision: "abc123"},
		{Instance: "nfqws1-main", Engine: "nfqws1", Strategy: "alt4", Revision: "def456"},
	})
	if !reflect.DeepEqual(metadata.DomainLists, []string{"example"}) {
		t.Errorf("domain lists = %#v", metadata.DomainLists)
	}
	if !reflect.DeepEqual(metadata.IPLists, []string{"example"}) {
		t.Errorf("ip lists = %#v", metadata.IPLists)
	}
	if !reflect.DeepEqual(metadata.Providers, []string{"runetfreedom"}) {
		t.Errorf("providers = %#v", metadata.Providers)
	}
	if !reflect.DeepEqual(metadata.Pools, []string{"vpn_a", "vpn_b"}) {
		t.Errorf("pools = %#v", metadata.Pools)
	}
	if metadata.Strategies[0].Strategy != "alt12" {
		t.Errorf("strategies are not sorted: %#v", metadata.Strategies)
	}
	if metadata.Paths["configuration"] != DefaultPath {
		t.Errorf("paths = %#v", metadata.Paths)
	}
	if len(metadata.ChainActions) != len(ChainActions) {
		t.Errorf("chain actions = %#v", metadata.ChainActions)
	}
}

func TestBuildMetadataOnEmptyConfig(t *testing.T) {
	metadata := BuildMetadata(nil, nil, nil, nil)
	if metadata.DomainLists == nil || metadata.Pools == nil || metadata.Strategies == nil {
		t.Fatalf("empty metadata must serialise as empty lists, got %#v", metadata)
	}
}

// §4: a UI must be able to offer a RULE flag and a chain action from different
// lists, because they render in different places — the section form and the ladder.
// The test that a value exists is not the test that it is in the RIGHT list: serving
// `bypass` among the actions is exactly the confusion this separated.
func TestMetadataKeepsRuleFlagsOutOfTheChainActions(t *testing.T) {
	meta := BuildMetadata(nil, nil, nil, nil)
	for _, a := range meta.ChainActions {
		if a.Name == "bypass" {
			t.Error("bypass is served as a chain action; it is a rule flag, and a rung that is " +
				"the absence of a rule cannot be escalated to")
		}
	}
	var found bool
	for _, f := range meta.RuleFlags {
		if f.Name == "bypass" {
			found = true
			if f.Description == "" {
				t.Error("a rule flag with no description; the GUI has nothing to put next to the switch")
			}
		}
	}
	if !found {
		t.Error("bypass is missing from the rule flags")
	}
	// `direct` stays an action: "no proxy, no desync" IS a path the ladder can take.
	var direct bool
	for _, a := range meta.ChainActions {
		if a.Name == "direct" {
			direct = true
		}
	}
	if !direct {
		t.Error("direct was moved out of the chain actions, but it is a path")
	}
}
