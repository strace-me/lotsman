package blockcheck

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategy"
)

// ParseEnumerated extracts every strategy blockcheck *tried*, not just the
// winners — the full search space. Each attempt is a line of the form
// "- <test> ipv<N> <domain> : <daemon> <args...>". This is how we harvest
// zapret's curated strategy list: run blockcheck in SIMULATE/force (no network,
// no nfqws) and import everything it would have tested. Winners come from
// ParseSummary/ParseWorking instead.
func ParseEnumerated(out string) []Result {
	var results []Result
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		if r, ok := parseStrategyLine(strings.TrimSpace(line[2:])); ok {
			results = append(results, r)
		}
	}
	return results
}

// Dedup collapses results with the same daemon+args (the same strategy found for
// several domains/tests) to one, keeping the first occurrence.
func Dedup(in []Result) []Result {
	seen := map[string]bool{}
	out := make([]Result, 0, len(in))
	for _, r := range in {
		key := r.Daemon + " " + strings.Join(r.Args, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// ToDefinitions converts harvested nfqws strategies into catalog definitions
// (tpws is dropped — see ZapretResults). Each gets a deterministic ID derived
// from its args, so re-harvesting the same strategy yields the same ID and the
// KB's history for it survives. Definitions carry NFQWSArgs (a single-profile
// strategy) to be rendered into a launcher before use.
func ToDefinitions(results []Result) []strategy.Definition {
	zap := Dedup(ZapretResults(results))
	defs := make([]strategy.Definition, 0, len(zap))
	for _, r := range zap {
		defs = append(defs, strategy.Definition{
			ID:        definitionID(r.Args),
			Class:     strategy.ClassZapret,
			NFQWSArgs: r.Args,
			Notes:     fmt.Sprintf("discovered via %s on %s ipv%d", r.Test, r.Domain, r.IPV),
		})
	}
	return defs
}

// definitionID builds a stable, readable-ish ID for a discovered strategy:
// "disc-<desync-mode>-<hash>", or "disc-<hash>" when there is no --dpi-desync
// mode (e.g. a bare --hostcase). The hash makes it collision-resistant and
// deterministic across runs; the mode prefix keeps logs legible.
func definitionID(args []string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.Join(args, " ")))
	sum := h.Sum32()
	if mode := desyncMode(args); mode != "" {
		return fmt.Sprintf("disc-%s-%08x", mode, sum)
	}
	return fmt.Sprintf("disc-%08x", sum)
}

// desyncMode returns the --dpi-desync value (commas turned to underscores for a
// clean ID), or "" if absent.
func desyncMode(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--dpi-desync="); ok {
			return strings.ReplaceAll(v, ",", "_")
		}
	}
	return ""
}
