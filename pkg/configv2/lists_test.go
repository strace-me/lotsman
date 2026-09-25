package configv2

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/lists"
)

func TestListsDefinitions(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	domains, ips := cfg.Lists.Definitions()
	domain, ok := domains["example"]
	if !ok {
		t.Fatal("domain list was not mapped")
	}
	if domain.Kind != lists.Domains || domain.Source != "runetfreedom" || domain.Category != "example" {
		t.Fatalf("domain definition = %#v", domain)
	}
	if domain, ok := ips["example"]; !ok || domain.Kind != lists.IPs {
		t.Fatalf("ip definition = %#v", domain)
	}
}
