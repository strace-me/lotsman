package capture

import (
	"strings"
	"testing"
)

// liveSingboxTable is the canonical `nft list table ip singbox` from the R5S
// (tab-indented, as nft prints). TM-2a must reproduce it byte-for-byte.
var liveSingboxTable = strings.Join([]string{
	"table ip singbox {",
	"\tchain prerouting {",
	"\t\ttype filter hook prerouting priority mangle; policy accept;",
	"\t\tip daddr { 10.0.0.0/8, 127.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 } return",
	"\t\tmeta mark 0x000000ff return",
	"\t\tip daddr { 198.51.100.10, 203.0.113.20 } return",
	"\t\tip daddr 192.0.2.0/24 return",
	"\t\tudp dport { 67, 68 } return",
	"\t\tiifname \"br-lan\" meta l4proto tcp tproxy to 127.0.0.1:7893 meta mark set 0x00000001 accept",
	"\t\tiifname \"br-lan\" meta l4proto udp tproxy to 127.0.0.1:7893 meta mark set 0x00000001 accept",
	"\t}",
	"}",
	"",
}, "\n")

func TestGenerateNftByteIdenticalToLive(t *testing.T) {
	got := string(GenerateNft(DefaultModel()))
	if got != liveSingboxTable {
		t.Errorf("GenerateNft != live table.\n--- got ---\n%s\n--- want ---\n%s", got, liveSingboxTable)
	}
}

// Set ordering must be nft-canonical (by IP value), regardless of model input order.
func TestGenerateNftSortsSetsCanonically(t *testing.T) {
	m := DefaultModel()
	m.LocalCIDRs = []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "127.0.0.0/8"} // shuffled
	m.LoopBypassIPs = []string{"203.0.113.20", "198.51.100.10"}                            // reversed
	if got := string(GenerateNft(m)); got != liveSingboxTable {
		t.Errorf("shuffled model must still render canonical order:\n%s", got)
	}
}

func TestSingleElementSetsHaveNoBraces(t *testing.T) {
	out := string(GenerateNft(DefaultModel()))
	if !strings.Contains(out, "ip daddr 192.0.2.0/24 return") {
		t.Error("a single-element daddr must render without braces")
	}
	// one UDP port -> no braces
	m := DefaultModel()
	m.BypassUDPPorts = []int{67}
	if !strings.Contains(string(GenerateNft(m)), "udp dport 67 return") {
		t.Error("a single UDP port must render without braces")
	}
}

func TestBypassRulesRenderBeforeTproxy(t *testing.T) {
	m := DefaultModel()
	m.BypassSets = []Bypass{
		{Name: "discord-voice", Proto: "udp", DstCIDRs: []string{"66.22.192.0/18"}, DstPorts: []string{"50000-65535"}},
		{Name: "xbox-game", Proto: "udp", DstPorts: []string{"3074", "30000-45000"}},
		{Name: "valve", DstCIDRs: []string{"155.133.248.0/24"}}, // ip-only, any proto
	}
	out := string(GenerateNft(m))

	wantVoice := "ip daddr 66.22.192.0/18 udp dport 50000-65535 return"
	wantXbox := "udp dport { 3074, 30000-45000 } return"
	wantValve := "ip daddr 155.133.248.0/24 return"
	for _, w := range []string{wantVoice, wantXbox, wantValve} {
		if !strings.Contains(out, w) {
			t.Errorf("missing bypass rule %q in:\n%s", w, out)
		}
	}
	// Each bypass return must come BEFORE the tproxy rule (kernel-direct wins).
	tproxyIdx := strings.Index(out, "tproxy to")
	for _, w := range []string{wantVoice, wantXbox, wantValve} {
		if strings.Index(out, w) > tproxyIdx {
			t.Errorf("bypass %q must precede tproxy rule", w)
		}
	}
}

func TestDefaultModelHasNoBypass(t *testing.T) {
	// TM-3 must not change the byte-identical default (no BypassSets => legacy table).
	if string(GenerateNft(DefaultModel())) != liveSingboxTable {
		t.Error("DefaultModel (no bypass) must stay byte-identical to live")
	}
}

func TestDeviceOverrideBypassBeforeTproxy(t *testing.T) {
	m := DefaultModel()
	m.DeviceBypass = []Device{{Name: "living-room-tv", SrcCIDRs: []string{"192.168.1.50"}}}
	out := string(GenerateNft(m))
	want := "ip saddr 192.168.1.50 return"
	if !strings.Contains(out, want) {
		t.Fatalf("missing device-override rule %q in:\n%s", want, out)
	}
	if strings.Index(out, want) > strings.Index(out, "tproxy to") {
		t.Error("device-override return must precede tproxy")
	}
}

func TestDefaultModelHasNoDeviceOverride(t *testing.T) {
	if string(GenerateNft(DefaultModel())) != liveSingboxTable {
		t.Error("DefaultModel (no device override) must stay byte-identical to live")
	}
}

// Bug-hunt fix: nft re-canonicalizes a port set into numeric order, so a bypass
// with unsorted/range ports must be emitted sorted by the low end — else it diffs
// against `nft list` forever and churns the reconcile (LOT-1 class).
func TestPortSetSortedByLowEnd(t *testing.T) {
	m := DefaultModel()
	m.BypassSets = []Bypass{{Name: "x", Proto: "udp", DstPorts: []string{"30000-45000", "3478-3481", "443"}}}
	out := string(GenerateNft(m))
	if !strings.Contains(out, "udp dport { 443, 3478-3481, 30000-45000 } return") {
		t.Errorf("port set not sorted by low end:\n%s", out)
	}
}

// Bug-hunt fix: the table is ip (IPv4) family — IPv6/malformed members must be
// dropped (rendering them = invalid nft that fails `nft -c`, stalling capture).
func TestDaddrSetDropsNonIPv4(t *testing.T) {
	m := DefaultModel()
	m.BypassSets = []Bypass{
		{Name: "mixed", Proto: "udp", DstCIDRs: []string{"2001:db8::/32", "1.2.3.4/32"}, DstPorts: []string{"443"}},
		{Name: "v6only", DstCIDRs: []string{"2001:db8::/32"}}, // all-IPv6 -> no rule at all
	}
	out := string(GenerateNft(m))
	if strings.Contains(out, "2001:db8") {
		t.Errorf("IPv6 member leaked into ip-family table:\n%s", out)
	}
	if !strings.Contains(out, "ip daddr 1.2.3.4/32 udp dport 443 return") {
		t.Errorf("IPv4 member of a mixed set must survive:\n%s", out)
	}
	// The all-IPv6 bypass has no valid matcher -> renders no rule (not a broken "ip daddr  return").
	if strings.Contains(out, "ip daddr  ") || strings.Contains(out, "ip daddr {  }") {
		t.Errorf("empty daddr set produced a malformed rule:\n%s", out)
	}
}
