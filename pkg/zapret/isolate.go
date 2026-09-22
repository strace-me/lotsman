package zapret

import (
	"fmt"
	"strings"
)

// TuneMark is the fwmark the tuner's probe sets on its own sockets (SO_MARK).
// It is the only thing that distinguishes a measurement from real traffic, so
// every rule below keys on it.
const TuneMark = 0x4554

// DesyncFwmark is the fwmark nfqws stamps on the packets it INJECTS to desync a
// connection (its --dpi-desync-fwmark; default 0x40000000). Unlike TuneMark it
// marks the ENGINE's own output, not the probe's input, and on a tun client that
// output must be kept out of the tunnel or it loops: the injected copy follows the
// normal route, auto_route pulls it in, and the DPI sees a duplicated, split
// ClientHello and answers RST while every log reports the strategy healthy.
//
// Shared here because BOTH engines need it excluded — production and the sandbox
// lane — and the rule must outlive whichever one is running: measured 2026-09-22,
// the lane gave a false negative on a recipe that works precisely when production
// was unarmed and had removed the rule.
const DesyncFwmark = 0x40000000

// IsolateOptions describes a sandbox for measuring a candidate desync strategy
// against live DPI without changing what anyone else's traffic gets.
type IsolateOptions struct {
	Table string // sandbox table, e.g. "inet lotsman_tune"
	WAN   string // egress interface
	QNum  int    // queue the CANDIDATE engine binds; must differ from production's
	Mark  int    // fwmark identifying probe traffic; 0 = TuneMark
	TCP   []string
	UDP   []string
	Bytes int // ct original packets 1-N, mirroring production
	Prio  int // hook priority; must run BEFORE the production table
}

// GenerateIsolateNft renders the sandbox table: probe-marked egress goes to the
// candidate's queue, everything else is untouched.
//
// The other half lives in the production table, and it is already there: both
// tables sit on the same hook and both run, so once the candidate engine accepts
// a packet, traversal would continue into the production table and queue it
// again — the probe desynced twice, by two different strategies, describing
// neither. GenerateNft therefore emits `meta mark <TuneMark> return` ahead of its
// queue rules.
//
// That table is Lotsman's on BOTH platforms, which took a wrong turn to
// establish: the client installs it from GenerateNft, and on the router
// GenerateInit writes the init script that installs it from the same renderer.
// Flowseal supplies lists and payloads; active.sh only launches nfqws with argv.
// There was never an ownership to take back.
func GenerateIsolateNft(o IsolateOptions) string {
	if o.Mark == 0 {
		o.Mark = TuneMark
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table %s {\n", o.Table)
	b.WriteString("    chain post {\n")
	fmt.Fprintf(&b, "        type filter hook postrouting priority %d; policy accept;\n", o.Prio)
	// Anything unmarked leaves immediately: a sandbox that could touch real
	// traffic is not a sandbox, and this is the household's only uplink.
	fmt.Fprintf(&b, "        meta mark != 0x%x return\n", o.Mark)
	writeRule(&b, o.WAN, "tcp", o.TCP, o.QNum, o.Bytes)
	writeRule(&b, o.WAN, "udp", o.UDP, o.QNum, o.Bytes)
	b.WriteString("    }\n}\n")
	return b.String()
}

// ProductionSkipRule is the rule the production table needs so probe traffic
// reaches the sandbox queue and nothing else. It must sit BEFORE that table's
// queue rules; appended after them it never matches, and the probe is desynced
// twice while every log says the sandbox is working.
func ProductionSkipRule(mark int) string {
	if mark == 0 {
		mark = TuneMark
	}
	return fmt.Sprintf("meta mark 0x%x return", mark)
}
