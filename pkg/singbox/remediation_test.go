package singbox

import (
	"bytes"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// sampleGenInputs returns a representative set of generator inputs (a service
// matched by rule-set + domain + ip, one node, one pool) reused by the
// remediation tests.
func sampleGenInputs(t *testing.T) ([]registry.Service, []subscription.Node, map[string][]subscription.Node) {
	t.Helper()
	hy2 := mustParse(t, "hysteria2://pw@1.2.3.4:443?sni=x.example", subscription.FormatSingleURL)
	yt := svc("youtube", "vpn_url_test", "geosite-youtube")
	yt.Domains = []string{"googlevideo.com"}
	yt.IPs = []string{"142.250.0.0/15"}
	return []registry.Service{yt}, []subscription.Node{hy2}, map[string][]subscription.Node{"vpn_url_test": {hy2}}
}

// TestRemediationNilUnchanged proves that passing no remediation (nil map, or an
// absent service) yields byte-identical output to the default generator — the
// PROPOSE-ONLY guarantee for LOT-18a (nothing passed => live config unchanged).
func TestRemediationNilUnchanged(t *testing.T) {
	services, nodes, memberships := sampleGenInputs(t)

	base, err := Generate(services, nil, nodes, memberships, DefaultOptions())
	if err != nil {
		t.Fatalf("base generate: %v", err)
	}

	// nil map => unchanged.
	optsNil := DefaultOptions()
	optsNil.Remediations = nil
	gotNil, err := Generate(services, nil, nodes, memberships, optsNil)
	if err != nil {
		t.Fatalf("nil-remediation generate: %v", err)
	}
	if !bytes.Equal(base.JSON, gotNil.JSON) {
		t.Error("nil Remediations changed the generated config (must be byte-identical)")
	}

	// Non-nil map but no entry for this service => still unchanged.
	optsEmpty := DefaultOptions()
	optsEmpty.Remediations = map[string]Remediation{"discord": {RejectQUIC: true}}
	gotEmpty, err := Generate(services, nil, nodes, memberships, optsEmpty)
	if err != nil {
		t.Fatalf("empty-entry generate: %v", err)
	}
	if !bytes.Equal(base.JSON, gotEmpty.JSON) {
		t.Error("remediation for an absent service changed this service's config (must be byte-identical)")
	}
}

// TestRemediationRejectQUIC checks that RejectQUIC emits udp/443 reject rules
// matching the service, placed BEFORE the service's normal routing rules.
func TestRemediationRejectQUIC(t *testing.T) {
	services, nodes, memberships := sampleGenInputs(t)
	opts := DefaultOptions()
	opts.Remediations = map[string]Remediation{"youtube": {RejectQUIC: true}}

	res, err := Generate(services, nil, nodes, memberships, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rules := routeRules(t, res.JSON)

	// Find reject rules and the first normal youtube-selector rule.
	firstReject, firstNormal := -1, -1
	rejectKinds := map[string]bool{}
	for i, r := range rules {
		if r["action"] == "reject" && hasUDP443(r) {
			if firstReject < 0 {
				firstReject = i
			}
			for _, k := range []string{"rule_set", "domain_suffix", "ip_cidr"} {
				if _, ok := r[k]; ok {
					rejectKinds[k] = true
				}
			}
		}
		if firstNormal < 0 && r["outbound"] == registry.SelectorTag("youtube") {
			firstNormal = i
		}
	}

	if firstReject < 0 {
		t.Fatal("no udp/443 reject rule emitted for RejectQUIC")
	}
	if firstNormal < 0 {
		t.Fatal("no normal youtube selector rule found")
	}
	if firstReject >= firstNormal {
		t.Errorf("reject-QUIC rule at %d must precede normal rule at %d (so it wins for udp/443)", firstReject, firstNormal)
	}
	// One reject rule per match kind the service declares (rule_set/domain/ip).
	for _, k := range []string{"rule_set", "domain_suffix", "ip_cidr"} {
		if !rejectKinds[k] {
			t.Errorf("reject-QUIC missing a %q rule (service declares it)", k)
		}
	}
}

// TestRemediationFallbackCIDRs checks that FallbackCIDRs emits an ip_cidr rule
// routing those CIDRs to the service's sel-<svc> selector.
func TestRemediationFallbackCIDRs(t *testing.T) {
	services, nodes, memberships := sampleGenInputs(t)
	opts := DefaultOptions()
	opts.Remediations = map[string]Remediation{
		"youtube": {FallbackCIDRs: []string{"172.217.0.0/16", "142.251.0.0/16"}},
	}

	res, err := Generate(services, nil, nodes, memberships, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rules := routeRules(t, res.JSON)

	found := false
	for _, r := range rules {
		if r["outbound"] != registry.SelectorTag("youtube") {
			continue
		}
		ipc, ok := r["ip_cidr"].([]any)
		if !ok {
			continue
		}
		set := map[string]bool{}
		for _, c := range ipc {
			set[c.(string)] = true
		}
		if set["172.217.0.0/16"] && set["142.251.0.0/16"] {
			found = true
		}
	}
	if !found {
		t.Error("no ip_cidr rule routing the FallbackCIDRs to sel-youtube")
	}
}

// TestRemediationDirectOnlyNoFallback ensures a direct-only service (no selector)
// does not get an IP-fallback rule (it would point at a nonexistent selector).
func TestRemediationDirectOnlyNoFallback(t *testing.T) {
	ro := registry.Service{
		Name:    "rudirect",
		Domains: []string{"gosuslugi.ru"},
		Chain:   []registry.ChainStep{{State: registry.StateLocked, StrategyClass: "direct", StrategyID: "direct"}},
	}
	if !ro.DirectOnly() {
		t.Fatal("test setup: service is not DirectOnly")
	}
	opts := DefaultOptions()
	opts.Remediations = map[string]Remediation{"rudirect": {FallbackCIDRs: []string{"1.2.3.0/24"}}}

	res, err := Generate([]registry.Service{ro}, nil, nil, nil, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, r := range routeRules(t, res.JSON) {
		if ipc, ok := r["ip_cidr"].([]any); ok {
			for _, c := range ipc {
				if c.(string) == "1.2.3.0/24" {
					t.Error("direct-only service got an IP-fallback rule (no selector exists)")
				}
			}
		}
	}
}

func hasUDP443(r map[string]any) bool {
	nets, _ := r["network"].([]any)
	ports, _ := r["port"].([]any)
	udp := false
	for _, n := range nets {
		if n == "udp" {
			udp = true
		}
	}
	p443 := false
	for _, p := range ports {
		if f, ok := p.(float64); ok && f == 443 {
			p443 = true
		}
	}
	return udp && p443
}
