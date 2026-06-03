package coherence

import (
	"reflect"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

func zapretSvc(name string, domains, ruleSets, ips []string) registry.Service {
	return registry.Service{
		Name: name, Domains: domains, RuleSets: ruleSets, IPs: ips,
		Chain: []registry.ChainStep{
			{State: "PREFERRED", StrategyClass: strategy.ClassZapret},
			{State: "VPN", StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
		},
	}
}

func vpnSvc(name string, domains []string) registry.Service {
	return registry.Service{
		Name: name, Domains: domains,
		Chain: []registry.ChainStep{{State: "VPN", StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"}},
	}
}

func TestAnalyzeFeedsOnlyZapretDomains(t *testing.T) {
	svcs := []registry.Service{
		zapretSvc("gaming", []string{"battle.net", "Blizzard.com", "*.epicgames.com"}, nil, nil),
		vpnSvc("social", []string{"instagram.com"}), // VPN: must NOT be fed to nfqws
	}
	got := Analyze(svcs, EngineFacts{})
	want := []string{"battle.net", "blizzard.com", "epicgames.com"} // lower-cased, *. stripped, sorted
	if !reflect.DeepEqual(got.NfqwsDomains, want) {
		t.Errorf("NfqwsDomains = %v, want %v", got.NfqwsDomains, want)
	}
	for _, d := range got.NfqwsDomains {
		if d == "instagram.com" {
			t.Error("a VPN service's domains must not be fed to nfqws")
		}
	}
	if len(got.Gaps) != 0 {
		t.Errorf("clean zapret service should have no gaps, got %v", got.Gaps)
	}
}

func TestAnalyzeGaps(t *testing.T) {
	svcs := []registry.Service{
		zapretSvc("rs", nil, []string{"geosite-x"}, nil),   // ruleset-only zapret -> hole
		zapretSvc("ip", nil, nil, []string{"1.2.3.0/24"}),  // ip-only zapret -> hole
		zapretSvc("ex", []string{"blocked.com"}, nil, nil), // domain in exclude -> overlap
	}
	facts := EngineFacts{NfqwsExcludeDomains: map[string]bool{"blocked.com": true}}
	got := Analyze(svcs, facts)

	kinds := map[string]string{} // service -> kind
	for _, g := range got.Gaps {
		kinds[g.Service] = g.Kind
	}
	if kinds["rs"] != GapRuleSetZapret {
		t.Errorf("rs gap = %q, want %q", kinds["rs"], GapRuleSetZapret)
	}
	if kinds["ip"] != GapIPZapret {
		t.Errorf("ip gap = %q, want %q", kinds["ip"], GapIPZapret)
	}
	if kinds["ex"] != GapExcludeOverlap {
		t.Errorf("ex gap = %q, want %q", kinds["ex"], GapExcludeOverlap)
	}
	// the excluded domain must NOT be fed (nfqws would skip it anyway).
	for _, d := range got.NfqwsDomains {
		if d == "blocked.com" {
			t.Error("an excluded domain must not be fed to nfqws")
		}
	}
}

func TestAnalyzeDedupes(t *testing.T) {
	svcs := []registry.Service{
		zapretSvc("a", []string{"x.com", "y.com"}, nil, nil),
		zapretSvc("b", []string{"y.com", "z.com"}, nil, nil), // y.com shared
	}
	got := Analyze(svcs, EngineFacts{})
	want := []string{"x.com", "y.com", "z.com"}
	if !reflect.DeepEqual(got.NfqwsDomains, want) {
		t.Errorf("deduped domains = %v, want %v", got.NfqwsDomains, want)
	}
}
