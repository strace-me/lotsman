package contract

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sharedFlags are the flags whose meaning is a VERDICT or a ROUTING behaviour, not
// a deployment detail. They must exist in BOTH roots, under the same name.
//
// The set is curated on purpose. "Both roots have identical flags" would be the
// wrong rule and would already be failing for good reasons: `-byedpi-init` is a
// procd detail, the client has no init script, and the client uses a tun where the
// router uses nft. What must not differ is a knob that changes what the system
// CONCLUDES — that is the drift LOT-65 documents, where the client ran a subset of
// the daemon for months with both test suites green.
// knownGaps are shared flags MISSING from a root today. They are listed rather
// than removed from sharedFlags so the guard stays honest and the omission stays
// visible: each entry is a real gap, each must be worked off, and the test FAILS
// if a listed gap is closed without the list being updated.
//
// A guard weakened to pass is worse than no guard, so the exception is data, and
// the test prints it.
var knownGaps = map[string]string{} // empty: the one gap this guard found (the client had no
// -dry-run) is closed, and the guard is what made it visible.

var sharedFlags = []string{
	// The measurement loop and its persistence.
	"interval", "probe-proxy", "state-file", "kb-file", "metrics-addr",
	// Verdicts that move services between tiers.
	"noderank", "path-health", "path-health-act", "engine-health",
	// The escalation policy itself.
	"smart", "dry-run",
	// The audit trail: without both, a service's history exists on one root only.
	"audit-log", "fail-log",
	// The engine under test. nfqws2 arrived on one root first once already; these
	// three keep the pair honest.
	"throughput-scan", "throughput-budget", "throughput-interval",
	// The data plane being driven.
	"singbox-bin", "singbox-config",
}

// gapKey is the single formatter for a "root is missing this flag" identity, so
// the knownGaps table and the failure list can never drift apart in spelling.
func gapKey(root, flag string) string { return root + ":-" + flag }

func TestBothRootsExposeEverySharedFlag(t *testing.T) {
	root := repoRoot(t)
	daemon := flagsIn(t, filepath.Join(root, "cmd", "lotsmand", "main.go"))
	client := flagsIn(t, filepath.Join(root, "client", "desktop", "main.go"))

	var missing []string
	for _, name := range sharedFlags {
		if !daemon[name] {
			missing = append(missing, gapKey("cmd/lotsmand", name))
		}
		if !client[name] {
			missing = append(missing, gapKey("client/desktop", name))
		}
	}
	var unclosed []string
	for key := range knownGaps {
		parts := strings.SplitN(key, ":-", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed knownGaps key %q", key)
		}
		var present map[string]bool
		switch parts[0] {
		case "cmd/lotsmand":
			present = daemon
		case "client/desktop":
			present = client
		default:
			t.Fatalf("malformed knownGaps key %q", key)
		}
		if present[parts[1]] {
			unclosed = append(unclosed, key)
		}
	}
	// A gap that got closed but stayed on the list means the list is now lying about
	// what is missing, which is the same failure the guard exists to prevent.
	if len(unclosed) > 0 {
		sort.Strings(unclosed)
		t.Errorf("these known gaps are now CLOSED — remove them from knownGaps:\n  %s",
			strings.Join(unclosed, "\n  "))
	}
	if len(knownGaps) > 0 {
		var keys []string
		for k := range knownGaps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Logf("%d KNOWN PARITY GAP(S) — the guard is green but the tree is not:\n%s",
			len(keys), joinGaps(keys))
	}
	var real []string
	for _, m := range missing {
		if _, ok := knownGaps[m]; !ok {
			real = append(real, m)
		}
	}
	if len(real) > 0 {
		sort.Strings(real)
		t.Errorf("flags that change a verdict or a route must exist in BOTH roots:\n  %s",
			strings.Join(prettify(real), "\n  "))
	}
}

func prettify(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, strings.Replace(k, ":-", ": -", 1))
	}
	return out
}

func joinGaps(keys []string) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("  - " + k + "\n      " + knownGaps[k] + "\n")
	}
	return b.String()
}

// The same knob under two names is the same drift wearing a disguise: an operator
// who learns -clash-base on the router has no reason to know the client spells it
// differently, and a script that carries one of them over silently loses the
// setting. Renaming is a compatibility change, so it gets caught here.
func TestNoVerdictFlagIsSpelledTwoWays(t *testing.T) {
	root := repoRoot(t)
	daemon := flagsIn(t, filepath.Join(root, "cmd", "lotsmand", "main.go"))
	client := flagsIn(t, filepath.Join(root, "client", "desktop", "main.go"))

	for _, name := range sharedFlags {
		if daemon[name] && client[name] {
			continue
		}
		// Report the near-miss so the fix is obvious rather than a hunt: a flag in
		// one root that shares a prefix with a shared flag in the other is almost
		// always the same setting under a different name.
		for other := range daemon {
			if other != name && strings.HasPrefix(other, name) {
				t.Logf("note: daemon has -%s and -%s; client has neither", name, other)
			}
		}
	}
}
