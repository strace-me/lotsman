package zapret

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The capture spec and the desync profiles are two statements about the same
// traffic, and keeping them as two independent literals is how a profile ends up
// filtering ports the queue never carries: the profile exists, matches nothing,
// and says nothing. Measured — Discord voice asks for udp 50000-50100 while the
// queue carried only 443, so the voice profile was dead the moment it was written.
//
// So the argv is the source of truth and the capture is DERIVED from it.

// CaptureFromArgs reads the ports a composed nfqws/winws argv actually filters on,
// so the queue can be built to carry exactly them. It reads every profile in the
// argv (the --new-delimited blocks are just more arguments here).
func CaptureFromArgs(args []string) Capture {
	var c Capture
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--filter-tcp="); ok {
			c.TCP = append(c.TCP, splitPorts(v)...)
		}
		if v, ok := strings.CutPrefix(a, "--filter-udp="); ok {
			c.UDP = append(c.UDP, splitPorts(v)...)
		}
	}
	c.TCP = dedupPorts(c.TCP)
	c.UDP = dedupPorts(c.UDP)
	return c
}

// Union merges two capture specs. Used to keep a floor under the derived spec:
// the base ports must stay queued even when no current recipe names them, or a
// recipe swap would tear down capture for traffic another profile still wants.
func (c Capture) Union(o Capture) Capture {
	return Capture{
		TCP: dedupPorts(append(append([]string{}, c.TCP...), o.TCP...)),
		UDP: dedupPorts(append(append([]string{}, c.UDP...), o.UDP...)),
	}
}

// Equal reports whether two specs name the same port tokens. Token-level, not
// interval-level: it answers "does the installed table still match what we want
// to install", where the two are produced by the same code and so spell ranges
// the same way.
func (c Capture) Equal(o Capture) bool {
	return equalTokens(c.TCP, o.TCP) && equalTokens(c.UDP, o.UDP)
}

// Uncovered returns the ports `want` asks for that this capture does not carry —
// the coherence check between a composed strategy and the queue in front of it.
// Interval-aware, so a capture of 50000-50100 covers a profile asking for 50007.
// A non-empty result means some profile is dead and nothing else would say so.
func (c Capture) Uncovered(want Capture) Capture {
	return Capture{
		TCP: uncovered(c.TCP, want.TCP),
		UDP: uncovered(c.UDP, want.UDP),
	}
}

// PortCount is how many individual ports a spec claims — the size of its scope.
// Used to judge whether a domain-less profile is narrow enough to be legitimate:
// 19294-19344 plus 50000-50100 is 152 ports and is a targeted rule, 1024-65535 is
// most of the internet and is not.
func (c Capture) PortCount() int {
	n := 0
	for _, ivs := range [][]string{c.TCP, c.UDP} {
		for _, t := range ivs {
			lo, hi, ok := portRange(t)
			if !ok {
				continue
			}
			n += hi - lo + 1
		}
	}
	return n
}

func splitPorts(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func dedupPorts(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// portRange parses "443" or "50000-50100" into an inclusive interval.
func portRange(tok string) (lo, hi int, ok bool) {
	tok = strings.TrimSpace(tok)
	if l, h, found := strings.Cut(tok, "-"); found {
		a, err1 := strconv.Atoi(strings.TrimSpace(l))
		b, err2 := strconv.Atoi(strings.TrimSpace(h))
		if err1 != nil || err2 != nil || a > b {
			return 0, 0, false
		}
		return a, b, true
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return 0, 0, false
	}
	return n, n, true
}

// uncovered returns the tokens of want no interval of have contains.
func uncovered(have, want []string) []string {
	type iv struct{ lo, hi int }
	var haveIvs []iv
	for _, t := range have {
		if lo, hi, ok := portRange(t); ok {
			haveIvs = append(haveIvs, iv{lo, hi})
		}
	}
	var out []string
	for _, t := range want {
		lo, hi, ok := portRange(t)
		if !ok {
			continue
		}
		// Every port of the wanted interval must be inside SOME held interval.
		// Ranges here are small and few, so the straightforward scan is fine and
		// avoids an interval-merge that would be easy to get subtly wrong.
		covered := true
		for p := lo; p <= hi && covered; p++ {
			in := false
			for _, h := range haveIvs {
				if p >= h.lo && p <= h.hi {
					in = true
					break
				}
			}
			covered = in
		}
		if !covered {
			out = append(out, t)
		}
	}
	return out
}

// ShadowedBlock reports the first profile in a multi-block recipe whose filter an
// EARLIER profile already claims, so nfqws — which matches profiles first to last
// and stops — can never reach it.
//
// This is not a hypothetical. `flowseal-alt12-google` is three blocks: tcp/443
// hostfakesplit, then tcp/80,443 fake+multisplit with seqovl=664, then udp/443.
// Its author scoped those first two to DIFFERENT hostlists — Google domains one
// way, everything else the other — and the per-service model has one domain list
// per rule, which it substitutes into every block. Both blocks therefore carry the
// same domains, the first claims 443, and the second can only ever see port 80.
// YouTube does not use port 80. So the rule ran hostfakesplit alone all day under
// the name of a bundle whose whole point was the multisplit behind it, and the
// knowledge base scored the bundle for what one third of it did.
//
// A recipe that cannot do what its author wrote must be refused rather than
// quietly reduced — the same reasoning that already keeps ALT12's --ipset profiles
// out of the catalogue.
//
// A profile narrowed by --filter-l7 is not shadowed by one without it (or with a
// different set): the ports overlap but the traffic does not.
func ShadowedBlock(blocks [][]string) (index int, reason string, shadowed bool) {
	var claimed Capture
	var claimedL7 []string
	for i, b := range blocks {
		want := CaptureFromArgs(b)
		l7 := filterL7(b)
		if i > 0 && sameL7(l7, claimedL7) {
			if reach := claimed.Uncovered(want); reach.PortCount() < want.PortCount() {
				return i, fmt.Sprintf("profile %d filters %s but earlier profiles already claim %s, and nfqws stops at the first match",
					i+1, describe(want), describe(claimed)), true
			}
		}
		claimed = claimed.Union(want)
		claimedL7 = l7
	}
	return 0, "", false
}

func filterL7(args []string) []string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--filter-l7="); ok {
			return splitPorts(v) // same comma-separated shape
		}
	}
	return nil
}

func sameL7(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func describe(c Capture) string {
	var bits []string
	if len(c.TCP) > 0 {
		bits = append(bits, "tcp/"+strings.Join(c.TCP, ","))
	}
	if len(c.UDP) > 0 {
		bits = append(bits, "udp/"+strings.Join(c.UDP, ","))
	}
	if len(bits) == 0 {
		return "nothing"
	}
	return strings.Join(bits, " ")
}
