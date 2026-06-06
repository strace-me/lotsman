// Package capture is the TM-2 capture layer (docs/DESIGN-tm2-capture.md): it
// turns Lotsman into the OWNER of the tproxy nft table that pulls LAN traffic
// into sing-box — the third of the four data-plane layers (route + desync are
// already owned; capture + NAT were hardcoded in /usr/bin/sb-nft).
//
// Stage 1 is byte-identical: GenerateNft(DefaultModel()) reproduces the live
// `table ip singbox` exactly (canonical nft form), so the reconciler (TM-2b) can
// diff desired vs `nft list table` cleanly and prove parity before it ever
// applies. The model is the seam the later slices extend: TM-3 bypass adds
// `return` rules before tproxy; TM-4 NAT layers on top.
package capture

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Model is the capture layer's configurable inputs. DefaultModel mirrors the
// current /usr/bin/sb-nft on the R5S; fields are the seam TM-3/TM-4 extend.
type Model struct {
	Table      string // nft table name ("singbox")
	Chain      string // prerouting chain name
	LanIface   string // capture ingress iface ("br-lan")
	TproxyIP   string // sing-box tproxy listen ip ("127.0.0.1")
	TproxyPort int    // sing-box tproxy listen port (7893)
	Mark       uint32 // fwmark set on captured traffic (1) -> ip rule -> table 100
	SelfMark   uint32 // sing-box default_mark, returned so its own egress is not re-captured (0xff)

	LocalCIDRs     []string // private/loopback ranges sent straight through (return)
	LoopBypassIPs  []string // VPN server IPs returned to avoid a tproxy loop
	SinkholeCIDRs  []string // ranges returned (e.g. TEST-NET-1 sinkhole)
	BypassUDPPorts []int    // UDP dports returned before tproxy (e.g. DHCP 67,68)

	// BypassSets (TM-3) are the treatment=bypass matchers: NAT-sensitive traffic
	// (game-UDP, voice/RTC) that must go kernel-direct (return BEFORE tproxy), by
	// traffic CLASS — IP-set/port — never by device. Empty = no bypass (DefaultModel
	// stays byte-identical to the legacy capture).
	BypassSets []Bypass

	// DeviceBypass (TM-6) is the device-level OVERRIDE: traffic from these source
	// addresses skips tproxy (kernel-direct) regardless of class — the explicit
	// opt-in exception ("this TV is direct-only"), an override ON TOP of the
	// class-based default, never the default mechanism. Empty = none.
	DeviceBypass []Device
}

// Device is a device-level capture override (TM-6): its source addresses return
// before tproxy (kernel-direct). Use sparingly — it is the per-device escape
// hatch, not how treatment is normally decided (that is by traffic class).
type Device struct {
	Name     string   // label (parental control / "TV direct-only"), not emitted
	SrcCIDRs []string // device source IP/subnet (saddr)
}

// Bypass is one treatment=bypass matcher: traffic matching it returns before the
// tproxy rule, so it is never pulled into sing-box (kernel-direct, NAT-open via
// TM-4). At least one of DstCIDRs / DstPorts must be set; Proto scopes the ports.
type Bypass struct {
	Name     string   // label (for logs/provenance), not emitted
	Proto    string   // "udp" | "tcp" | "" (any; required if DstPorts set)
	DstCIDRs []string // destination IP/CIDR set (game servers, voice endpoints)
	DstPorts []string // dport values/ranges, e.g. "3478-3481", "30000-45000"
}

// DefaultModel is the live R5S capture configuration (the byte-identical target).
func DefaultModel() Model {
	return Model{
		Table: "singbox", Chain: "prerouting", LanIface: "br-lan",
		TproxyIP: "127.0.0.1", TproxyPort: 7893, Mark: 1, SelfMark: 0xff,
		LocalCIDRs:     []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
		LoopBypassIPs:  []string{"45.91.54.162", "192.0.2.12"},
		SinkholeCIDRs:  []string{"192.0.2.0/24"},
		BypassUDPPorts: []int{67, 68},
	}
}

// GenerateNft renders the capture table in canonical nft form (what `nft list
// table ip <name>` prints), so it diffs byte-for-byte against the live table.
// Set members are sorted by value to match nft's own canonical ordering.
func GenerateNft(m Model) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "table ip %s {\n", m.Table)
	fmt.Fprintf(&b, "\tchain %s {\n", m.Chain)
	b.WriteString("\t\ttype filter hook prerouting priority mangle; policy accept;\n")
	if len(m.LocalCIDRs) > 0 {
		fmt.Fprintf(&b, "\t\tip daddr %s return\n", daddrSet(m.LocalCIDRs))
	}
	fmt.Fprintf(&b, "\t\tmeta mark 0x%08x return\n", m.SelfMark)
	if len(m.LoopBypassIPs) > 0 {
		fmt.Fprintf(&b, "\t\tip daddr %s return\n", daddrSet(m.LoopBypassIPs))
	}
	if len(m.SinkholeCIDRs) > 0 {
		fmt.Fprintf(&b, "\t\tip daddr %s return\n", daddrSet(m.SinkholeCIDRs))
	}
	if len(m.BypassUDPPorts) > 0 {
		fmt.Fprintf(&b, "\t\tudp dport %s return\n", portSet(m.BypassUDPPorts))
	}
	// TM-6 device override: per-device kernel-direct, BEFORE class bypass and tproxy.
	for _, d := range m.DeviceBypass {
		if len(d.SrcCIDRs) > 0 {
			fmt.Fprintf(&b, "\t\tip saddr %s return\n", daddrSet(d.SrcCIDRs))
		}
	}
	// TM-3 bypass: kernel-direct returns for NAT-sensitive classes, BEFORE tproxy.
	for _, bp := range m.BypassSets {
		if rule := renderBypass(bp); rule != "" {
			fmt.Fprintf(&b, "\t\t%s\n", rule)
		}
	}
	for _, proto := range []string{"tcp", "udp"} {
		fmt.Fprintf(&b, "\t\tiifname %q meta l4proto %s tproxy to %s:%d meta mark set 0x%08x accept\n",
			m.LanIface, proto, m.TproxyIP, m.TproxyPort, m.Mark)
	}
	b.WriteString("\t}\n}\n")
	return []byte(b.String())
}

// daddrSet renders an ip-address match: a bare value for one element, a
// brace-set for many — members sorted by IP value (nft canonical order).
func daddrSet(items []string) string {
	s := append([]string{}, items...)
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(ipOf(s[i]), ipOf(s[j])) < 0 })
	if len(s) == 1 {
		return s[0]
	}
	return "{ " + strings.Join(s, ", ") + " }"
}

// ipOf extracts the 16-byte IP of an address or CIDR for sorting.
func ipOf(s string) net.IP {
	if ip, _, err := net.ParseCIDR(s); err == nil {
		return ip.To16()
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.To16()
	}
	return nil
}

// renderBypass builds one bypass `return` rule in canonical nft order
// (ip daddr … then <proto> dport … then return). Returns "" if it has no matcher.
func renderBypass(bp Bypass) string {
	var parts []string
	if len(bp.DstCIDRs) > 0 {
		parts = append(parts, "ip daddr "+daddrSet(bp.DstCIDRs))
	}
	if len(bp.DstPorts) > 0 && bp.Proto != "" {
		parts = append(parts, bp.Proto+" dport "+portStrSet(bp.DstPorts))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + " return"
}

// portStrSet renders string ports/ranges (e.g. "3478-3481") as a bare value or a
// brace-set, preserving input order (ranges have no single sort key).
func portStrSet(ports []string) string {
	if len(ports) == 1 {
		return ports[0]
	}
	return "{ " + strings.Join(ports, ", ") + " }"
}

func portSet(ports []int) string {
	p := append([]int{}, ports...)
	sort.Ints(p)
	strs := make([]string, len(p))
	for i, v := range p {
		strs[i] = strconv.Itoa(v)
	}
	if len(strs) == 1 {
		return strs[0]
	}
	return "{ " + strings.Join(strs, ", ") + " }"
}
