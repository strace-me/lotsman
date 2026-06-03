package dataplane

import (
	"strconv"
	"strings"
)

// Conn is per-connection quality read passively from the kernel's tcp_info via
// `ss -ti`. This is real user traffic — no synthetic packets — so it reflects
// the actual experienced path. The kernel gives a smoothed rtt and its variance
// (rttvar, a jitter proxy) plus retransmits (a loss proxy); sampling it over
// time yields percentiles for the active strategy without probing.
type Conn struct {
	Peer        string  // peer address:port
	RTTms       float64 // smoothed RTT
	RTTVarMs    float64 // RTT variance (jitter proxy)
	RetransTot  int     // total retransmitted segments
	DataSegsOut int     // data segments sent
}

// LossFraction estimates loss as retransmits / segments sent, in [0,1].
func (c Conn) LossFraction() float64 {
	if c.DataSegsOut <= 0 {
		return 0
	}
	f := float64(c.RetransTot) / float64(c.DataSegsOut)
	if f > 1 {
		return 1
	}
	return f
}

// ParseSS parses `ss -ti` output into per-connection tcp_info. A connection is
// a state line ("ESTAB ... local peer") followed by an indented stats line
// carrying "rtt:mean/var", "retrans:cur/total", "data_segs_out:N".
func ParseSS(output string) []Conn {
	var conns []Conn
	var peer string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// stats continuation for the last connection line
			if peer == "" || !strings.Contains(line, "rtt:") {
				continue
			}
			c := Conn{Peer: peer}
			for _, tok := range strings.Fields(line) {
				switch {
				case strings.HasPrefix(tok, "rtt:"):
					mean, vari := splitPair(strings.TrimPrefix(tok, "rtt:"))
					c.RTTms, c.RTTVarMs = mean, vari
				case strings.HasPrefix(tok, "retrans:"):
					_, total := splitPair(strings.TrimPrefix(tok, "retrans:"))
					c.RetransTot = int(total)
				case strings.HasPrefix(tok, "data_segs_out:"):
					c.DataSegsOut = atoi(strings.TrimPrefix(tok, "data_segs_out:"))
				}
			}
			conns = append(conns, c)
			peer = "" // consume; one stats line per connection
			continue
		}
		// connection line. The column layout varies (the State column is
		// dropped when filtering by state), so identify the peer as the last
		// address-like token (addr:port) rather than a fixed field index.
		fields := strings.Fields(line)
		if len(fields) == 0 || isSSHeader(fields[0]) {
			continue
		}
		if p := lastAddr(fields); p != "" {
			peer = p
		}
	}
	return conns
}

func isSSHeader(first string) bool {
	switch first {
	case "State", "Recv-Q", "Netid":
		return true
	}
	return false
}

// lastAddr returns the last field that looks like host:port (the peer), or "".
func lastAddr(fields []string) string {
	for i := len(fields) - 1; i >= 0; i-- {
		f := fields[i]
		// skip the header's "Address:Port" literal and process tokens
		if strings.Contains(f, ":") && !strings.Contains(f, "Address") && !strings.HasPrefix(f, "users:") {
			return f
		}
	}
	return ""
}

// splitPair parses "a/b" (or just "a") into two floats.
func splitPair(s string) (a, b float64) {
	x, y, found := strings.Cut(s, "/")
	a = atof(x)
	if found {
		b = atof(y)
	}
	return a, b
}

func atof(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }
func atoi(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
