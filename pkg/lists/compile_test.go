package lists

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompileAllEngines(t *testing.T) {
	snapshot := Snapshot{Name: "example", Kind: Domains, Values: []string{"a.example", "b.example"}}
	out, err := Compilers{
		HostlistCompiler{EngineName: "nfqws1"},
		HostlistCompiler{EngineName: "nfqws2"},
		SingboxRuleSetCompiler{},
	}.CompileAll(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("representations = %d", len(out))
	}
	for _, representation := range out[:2] {
		if representation.Content != "a.example\nb.example\n" {
			t.Errorf("%s content = %q", representation.Engine, representation.Content)
		}
	}
	var doc struct {
		Version int `json:"version"`
		Rules   []struct {
			DomainSuffix []string `json:"domain_suffix"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out[2].Content), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 3 || len(doc.Rules) != 1 || len(doc.Rules[0].DomainSuffix) != 2 {
		t.Fatalf("sing-box rule-set = %s", out[2].Content)
	}
}

func TestHostlistRejectsIPList(t *testing.T) {
	_, err := HostlistCompiler{EngineName: "nfqws1"}.Compile(Snapshot{Name: "example", Kind: IPs, Values: []string{"10.0.0.0/24"}})
	if err == nil {
		t.Fatal("hostlist compiler accepted an IP list")
	}
}

func TestSingboxIPListUsesCIDR(t *testing.T) {
	out, err := SingboxRuleSetCompiler{}.Compile(Snapshot{Name: "example", Kind: IPs, Values: []string{"10.0.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, `"ip_cidr":["10.0.0.0/24"]`) {
		t.Fatalf("content = %s", out.Content)
	}
}
