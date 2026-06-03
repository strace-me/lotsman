// Package zapret models nfqws instances as the unit Lotsman manages. One
// instance = one nfqws process on its own NFQUEUE number, capturing a defined
// set of ports and running a switchable strategy. Declaring a single instance
// gives the classic "one global nfqws" setup (Variant A); declaring several
// gives per-rule instances that switch and tune independently (Variant B).
// The user chooses simply by how many instances they declare.
package zapret

import (
	"fmt"
	"sort"
	"strings"
)

// Capture is the set of destination ports (or ranges, e.g. "50000-50100") an
// instance's nft rule sends to its queue.
type Capture struct {
	TCP []string
	UDP []string
}

// Instance is one nfqws process Lotsman manages.
type Instance struct {
	Name      string
	QNum      int
	Capture   Capture
	Connbytes int // first-N packets per connection to queue; 0 = whole stream
}

// NftOptions ground nft generation in the deployment.
type NftOptions struct {
	Table      string   // e.g. "inet zapret"
	WAN        string   // egress interface, e.g. "eth0"
	VPNServers []string // IPs to never desync (the VPN tunnels)
}

// DefaultNftOptions returns R5S-grounded defaults.
func DefaultNftOptions() NftOptions {
	return NftOptions{Table: "inet zapret", WAN: "eth0"}
}

// Validate checks the instance set is internally consistent: unique names and
// qnums, and no (protocol, port) captured by two instances — a packet can only
// go to one queue, so overlap would make routing ambiguous.
func Validate(instances []Instance) error {
	names := map[string]bool{}
	qnums := map[int]string{}
	owner := map[string]string{} // "tcp:443" -> instance name

	for _, in := range instances {
		if in.Name == "" {
			return fmt.Errorf("zapret: instance with empty name")
		}
		if names[in.Name] {
			return fmt.Errorf("zapret: duplicate instance name %q", in.Name)
		}
		names[in.Name] = true
		if in.QNum <= 0 {
			return fmt.Errorf("zapret: instance %q has invalid qnum %d", in.Name, in.QNum)
		}
		if prev, ok := qnums[in.QNum]; ok {
			return fmt.Errorf("zapret: instances %q and %q share qnum %d", prev, in.Name, in.QNum)
		}
		qnums[in.QNum] = in.Name

		for _, p := range expandPorts(in.Capture.TCP) {
			key := "tcp:" + p
			if prev, ok := owner[key]; ok {
				return fmt.Errorf("zapret: tcp port %s captured by both %q and %q (a packet can reach only one queue)", p, prev, in.Name)
			}
			owner[key] = in.Name
		}
		for _, p := range expandPorts(in.Capture.UDP) {
			key := "udp:" + p
			if prev, ok := owner[key]; ok {
				return fmt.Errorf("zapret: udp port %s captured by both %q and %q", p, prev, in.Name)
			}
			owner[key] = in.Name
		}
	}
	return nil
}

// GenerateNft renders the nft ruleset routing each instance's captured ports to
// its queue (with bypass + connbytes limit), excluding the VPN server IPs.
func GenerateNft(instances []Instance, opts NftOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table %s {\n", opts.Table)
	b.WriteString("    chain post {\n")
	b.WriteString("        type filter hook postrouting priority mangle; policy accept;\n")
	if len(opts.VPNServers) > 0 {
		fmt.Fprintf(&b, "        ip daddr { %s } return\n", strings.Join(opts.VPNServers, ", "))
	}
	for _, in := range instances {
		writeRule(&b, opts.WAN, "tcp", in.Capture.TCP, in.QNum, in.Connbytes)
		writeRule(&b, opts.WAN, "udp", in.Capture.UDP, in.QNum, in.Connbytes)
	}
	b.WriteString("    }\n}\n")
	return b.String()
}

func writeRule(b *strings.Builder, wan, proto string, ports []string, qnum, connbytes int) {
	if len(ports) == 0 {
		return
	}
	cb := ""
	if connbytes > 0 {
		cb = fmt.Sprintf("ct original packets 1-%d ", connbytes)
	}
	fmt.Fprintf(b, "        oifname %q meta l4proto %s %s dport { %s } %squeue num %d bypass\n",
		wan, proto, proto, strings.Join(ports, ", "), cb, qnum)
}

// expandPorts returns individual port tokens for overlap checking. Ranges like
// "50000-50100" are kept as a single token (range-vs-range overlap is not
// decomposed — exact-token collision is the common, cheap case to catch).
func expandPorts(ports []string) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, strings.TrimSpace(p))
	}
	sort.Strings(out)
	return out
}
