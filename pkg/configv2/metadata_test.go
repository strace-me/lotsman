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
