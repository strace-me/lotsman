package zapret

import (
	"fmt"
	"strings"
)

// TuneMark is the fwmark the tuner's probe sets on its own sockets (SO_MARK).
// It is the only thing that distinguishes a measurement from real traffic, so
// every rule below keys on it.
const TuneMark = 0x4554

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
// ⚠️ This table is only half of an isolated apply, and the other half is the
// reason the tuner has never been wired. Two tables on the same hook BOTH run:
// once the candidate engine accepts a packet from its queue, traversal continues
// into the production table, which queues it again — so the probe would be
// desynced twice, by two different strategies, and whatever it measured would
// describe neither. Isolation therefore requires a matching SKIP rule inside the
// PRODUCTION table (`meta mark <mark> return`, ahead of its queue rules), and
// that table belongs to whoever owns the data plane.
//
// On the desktop client Lotsman owns it (client/platform/nfqws installs it from
// GenerateNft) and the skip rule is ours to add. On the router it belongs to
// Flowseal's active.sh, which is precisely the ownership this project decided to
// take back and has not yet taken. So: renderer now, armed tuner after that.
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
