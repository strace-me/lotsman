package pools

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

func nodes() []subscription.Node {
	return []subscription.Node{
		{ID: "tcp-only", Protocol: subscription.ProtoVLESS, Caps: subscription.Caps{TCP: true}},
		{ID: "udp", Protocol: subscription.ProtoHysteria2, Caps: subscription.Caps{TCP: true, UDPNative: true}},
		{ID: "emerg-tcp", Protocol: subscription.ProtoTrojan, Caps: subscription.Caps{TCP: true}, Tags: []string{"emergency", "slow"}},
		{ID: "emerg-udp", Protocol: subscription.ProtoHysteria2, Caps: subscription.Caps{TCP: true, UDPNative: true}, Tags: []string{"emergency"}},
	}
}

func TestCountryFilter(t *testing.T) {
	ns := []subscription.Node{
		{ID: "nl", Caps: subscription.Caps{TCP: true}, Country: "nl"},
		{ID: "ru", Caps: subscription.Caps{TCP: true}, Country: "ru"},
		{ID: "smart", Caps: subscription.Caps{TCP: true}, Country: ""}, // smart location, unknown
	}
	// exclude RU: keeps nl + smart (unknown never excluded).
	excl := Pool{Filter: Filter{CountriesExclude: []string{"ru"}}}.Members(ns)
	got := ids(excl)
	if got["ru"] || !got["nl"] || !got["smart"] {
		t.Errorf("exclude ru = %v, want nl+smart, no ru", got)
	}
	// include only NL: keeps just nl (smart's unknown country is not nl).
	incl := Pool{Filter: Filter{CountriesInclude: []string{"nl"}}}.Members(ns)
	got = ids(incl)
	if !got["nl"] || got["ru"] || got["smart"] {
		t.Errorf("include nl = %v, want only nl", got)
	}
}

func ids(ns []subscription.Node) map[string]bool {
	m := make(map[string]bool, len(ns))
	for _, n := range ns {
		m[n.ID] = true
	}
	return m
}

func TestBuiltinPools(t *testing.T) {
	m := Builtin().Memberships(nodes())

	// vpn_url_test: tcp nodes, no emergency -> tcp-only, udp.
	got := ids(m["vpn_url_test"])
	if !got["tcp-only"] || !got["udp"] || got["emerg-tcp"] || got["emerg-udp"] {
		t.Errorf("vpn_url_test members = %v", got)
	}

	// vpn_url_test_udp: udp-native, no emergency -> udp only.
	got = ids(m["vpn_url_test_udp"])
	if !got["udp"] || got["tcp-only"] || got["emerg-udp"] {
		t.Errorf("vpn_url_test_udp members = %v", got)
	}

	// emergency_pool: emergency-tagged -> emerg-tcp, emerg-udp.
	got = ids(m["emergency_pool"])
	if !got["emerg-tcp"] || !got["emerg-udp"] || got["tcp-only"] || got["udp"] {
		t.Errorf("emergency_pool members = %v", got)
	}
}

func TestFilterSemantics(t *testing.T) {
	ns := nodes()

	// Caps are AND: requiring both tcp and udp_native drops tcp-only nodes.
	p := Pool{Filter: Filter{Caps: []string{CapTCP, CapUDPNative}}}
	if got := ids(p.Members(ns)); got["tcp-only"] || got["emerg-tcp"] {
		t.Errorf("caps AND failed: %v", got)
	}

	// TagsInclude is OR: include [slow,backup] matches the node tagged slow.
	p = Pool{Filter: Filter{TagsInclude: []string{"slow", "backup"}}}
	if got := ids(p.Members(ns)); !got["emerg-tcp"] || got["udp"] {
		t.Errorf("tags include OR failed: %v", got)
	}

	// Empty filter matches everything.
	if got := (Pool{}).Members(ns); len(got) != len(ns) {
		t.Errorf("empty filter matched %d, want %d", len(got), len(ns))
	}

	// Unknown capability matches nothing.
	p = Pool{Filter: Filter{Caps: []string{"quantum"}}}
	if got := p.Members(ns); len(got) != 0 {
		t.Errorf("unknown cap matched %d nodes, want 0", len(got))
	}
}
