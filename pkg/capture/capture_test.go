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
	"\t\tip daddr { 45.91.54.162, 192.0.2.12 } return",
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
	m.LoopBypassIPs = []string{"192.0.2.12", "45.91.54.162"}                            // reversed
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
