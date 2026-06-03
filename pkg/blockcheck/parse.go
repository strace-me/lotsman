// Package blockcheck drives zapret's blockcheck.sh — the discovery arm (Sapper).
// It runs blockcheck against a blocked target, parses which nfqws/tpws strategies
// got through, and hands the winners back so they can be learned (KB) and applied.
//
// Parsing targets blockcheck's machine-stable "* SUMMARY" block and the inline
// "!!!!! ... working strategy found ... !!!!!" headlines. The format was captured
// from a real run of blockcheck.sh (zapret 1.9.9a) in SIMULATE mode — see
// testdata/sim_quick_rutracker.txt.
package blockcheck

import (
	"strconv"
	"strings"
)

// Result is one strategy blockcheck found working for a (test, domain) pair.
type Result struct {
	Test   string   // blockcheck test function, e.g. "curl_test_https_tls12"
	IPV    int      // IP version: 4 or 6
	Domain string   // target domain
	Daemon string   // "nfqws" (zapret) or "tpws"
	Args   []string // the daemon's argument tokens (e.g. ["--dpi-desync=multisplit", "--dpi-desync-split-pos=1"])
}

// ParseSummary extracts the winning strategies from blockcheck's "* SUMMARY"
// block. Each summary line is "<test> ipv<N> <domain> : <daemon> <args...>". The
// block runs until the first blank or non-matching line (the trailing prose).
func ParseSummary(out string) []Result {
	lines := strings.Split(out, "\n")
	var results []Result
	inSummary := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if !inSummary {
			if strings.HasPrefix(line, "* SUMMARY") {
				inSummary = true
			}
			continue
		}
		if line == "" {
			break // blank line ends the SUMMARY block
		}
		r, ok := parseStrategyLine(line)
		if !ok {
			break // trailing prose ("Please note ...")
		}
		results = append(results, r)
	}
	return results
}

// workingMarker brackets blockcheck's inline announcement of a found strategy.
const workingMarker = "!!!!!"

// ParseWorking extracts strategies from the inline
// "!!!!! <test>: working strategy found for ipv<N> <domain> : <daemon> <args> !!!!!"
// headlines. These appear as blockcheck progresses; the SUMMARY is the deduped
// digest, but the headlines are useful when streaming a live run.
func ParseWorking(out string) []Result {
	var results []Result
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, workingMarker) || !strings.HasSuffix(line, workingMarker) {
			continue
		}
		body := strings.TrimSpace(strings.Trim(line, "!"))
		const sep = ": working strategy found for "
		i := strings.Index(body, sep)
		if i < 0 {
			continue
		}
		test := strings.TrimSpace(body[:i])
		// remainder: "ipv<N> <domain> : <daemon> <args>"
		r, ok := parseStrategyLine(body[i+len(sep):])
		if !ok {
			continue
		}
		r.Test = test
		results = append(results, r)
	}
	return results
}

// ZapretResults keeps only the nfqws (zapret) strategies — the ones Lotsman's
// zapret executor can apply. tpws is a separate engine and is dropped here.
func ZapretResults(in []Result) []Result {
	out := make([]Result, 0, len(in))
	for _, r := range in {
		if r.Daemon == "nfqws" {
			out = append(out, r)
		}
	}
	return out
}

// parseStrategyLine parses "<test?> ipv<N> <domain> : <daemon> <args...>". For
// SUMMARY lines the leading field is the test; for the headline remainder there
// is no leading test (it is set by the caller) and the line starts at "ipv<N>".
// Returns ok=false if the line is not a strategy line.
func parseStrategyLine(line string) (Result, bool) {
	left, right, found := strings.Cut(line, " : ")
	if !found {
		return Result{}, false
	}
	lf := strings.Fields(left)
	rf := strings.Fields(right)
	if len(rf) == 0 {
		return Result{}, false
	}

	var r Result
	// Locate the "ipv<N>" token; the field before it (if any) is the test, the
	// field after it is the domain.
	ipvIdx := -1
	for i, f := range lf {
		if strings.HasPrefix(f, "ipv") {
			if n, err := strconv.Atoi(strings.TrimPrefix(f, "ipv")); err == nil {
				r.IPV = n
				ipvIdx = i
				break
			}
		}
	}
	if ipvIdx < 0 || ipvIdx+1 >= len(lf) {
		return Result{}, false
	}
	if ipvIdx > 0 {
		r.Test = lf[ipvIdx-1]
	}
	r.Domain = lf[ipvIdx+1]
	r.Daemon = rf[0]
	r.Args = rf[1:]
	return r, true
}
