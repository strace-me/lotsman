package strategyimport

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// ErrEmpty means a block carried nothing but arguments we drop, so there is no
// strategy left to record.
var ErrEmpty = errors.New("no desync arguments left after normalization")

// Arguments that describe THIS deployment rather than the strategy: the queue it
// listens on, the global capture filter, how it daemonizes. They say nothing
// about how a strategy behaves, and keeping them would split one recipe into as
// many identities as there are boxes running it.
var dropExact = map[string]bool{
	"--daemon": true, "--nolog": true,
}

var dropPrefix = []string{
	"--qnum=", "--wf-tcp=", "--wf-udp=", "--wf-raw=", "--wf-l3=",
	"--pidfile=", "--user=", "--uid=", "--debug=", "--comment=",
	// Exclusions and auto-learning are Lotsman's to manage (per-service
	// exclude_domains, and the hostlist files it writes itself), not the
	// bundle's. Dropping an exclusion widens a block, which is only sound
	// because the other half of normalization narrows it: the bundle's global
	// host set becomes {{DOMAINS}}, which Lotsman fills with ONE service's
	// domains. A block that keeps a global scope keeps its own excludes, since
	// it has no host selector to be narrowed by.
	"--hostlist-exclude=", "--ipset-exclude=", "--hostlist-auto=", "--hostlist-auto-fail-threshold=",
}

// Flags that answer "which hosts is this pointed at". Bundles express it either
// as a file they ship or as an inline list; both are the same decision, and it
// is a decision Lotsman makes per service from the routing model. Collapsing
// both spellings to one placeholder is what lets the Windows and Linux editions
// of a strategy be recognised as the same recipe.
var hostSelectors = []string{"--hostlist=", "--hostlist-domains="}

const domainsPlaceholder = "--hostlist-domains={{DOMAINS}}"

// Arguments selecting traffic by IP set. Unlike an exclusion these are the
// block's POSITIVE target, and we have no way to express one: a block whose
// ipset is dropped no longer selects what its author wrote. The recipe is
// refused whole rather than repaired, because scoring a silently-altered recipe
// would put a verdict in the knowledge base about a strategy that never ran.
var unsupported = []string{"--ipset=", "--ipset-auto="}

var unresolvedVar = regexp.MustCompile(`\$\{?[A-Za-z_]`)

// Normalize turns one block's raw arguments into the canonical form the catalog
// stores: deployment-specific arguments removed, payload paths reduced to the
// bare filename, and the target host set replaced by a placeholder Lotsman fills
// per service. It returns an error when the block cannot be represented.
func Normalize(raw []string) ([]string, error) {
	var out []string
	for _, a := range raw {
		a = strings.Trim(a, `"'`)
		if a == "" || dropExact[a] {
			continue
		}
		if hasAnyPrefix(a, unsupported) {
			return nil, fmt.Errorf("%s is not expressible", flagName(a))
		}
		if hasAnyPrefix(a, dropPrefix) {
			continue
		}
		if hasAnyPrefix(a, hostSelectors) {
			// A block may name both a file and an inline list; one placeholder
			// says everything either of them said.
			if len(out) == 0 || out[len(out)-1] != domainsPlaceholder {
				out = append(out, domainsPlaceholder)
			}
			continue
		}
		// Payload references. Rather than enumerate the fake-* flags — the list
		// grows with every nfqws release, and this file would silently fall
		// behind — key on the value: a .bin is always a payload, and nfqws
		// resolves a bare filename against its working directory, which is the
		// one form that transfers between machines.
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasSuffix(strings.ToLower(v), ".bin") {
			a = k + "=" + basename(v)
		}
		if unresolvedVar.MatchString(a) {
			return nil, fmt.Errorf("unresolved variable in %q", a)
		}
		if !strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("stray token %q (not a flag)", a)
		}
		out = append(out, a)
	}
	if !hasDesync(out) {
		return nil, ErrEmpty
	}
	return out, nil
}

// hasDesync reports whether anything is left that actually mangles traffic. A
// block of nothing but filters is a selector, not a strategy.
func hasDesync(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--dpi-desync") || strings.HasPrefix(a, "--dup") ||
			strings.HasPrefix(a, "--orig-") || strings.HasPrefix(a, "--mss=") {
			return true
		}
	}
	return false
}

// basename strips a directory prefix written in either separator, plus the
// %~dp0 that batch files use for "next to me".
func basename(v string) string {
	v = strings.TrimPrefix(v, "%~dp0")
	if i := strings.LastIndexAny(v, `/\`); i >= 0 {
		v = v[i+1:]
	}
	return v
}

// Techniques derives the tag list from the arguments, so the tags cannot drift
// away from what the recipe actually does.
func Techniques(args []string) []string {
	s := " " + strings.Join(args, " ") + " "
	var tags []string
	add := func(t string) {
		for _, have := range tags {
			if have == t {
				return
			}
		}
		tags = append(tags, t)
	}
	if v, ok := value(s, "--dpi-desync="); ok {
		for _, mode := range strings.Split(v, ",") {
			add(mode)
		}
	}
	if strings.Contains(s, "--dpi-desync-split-seqovl=") {
		add("seqovl")
	}
	for _, f := range []struct{ flag, tag string }{
		{"--dpi-desync-fooling=", "fooling"},
		{"--dpi-desync-repeats=", "repeats"},
		{"--dpi-desync-cutoff=", "cutoff"},
	} {
		if v, ok := value(s, f.flag); ok {
			add(f.tag + ":" + v)
		}
	}
	for _, f := range []struct{ needle, tag string }{
		{"--dpi-desync-fake-quic=", "fake-quic"},
		{"--dpi-desync-fake-tls=", "fake-tls"},
		{"--dpi-desync-fake-unknown-udp=", "fake-unknown-udp"},
		{"--filter-l7=", "l7-filter"},
		{"--dpi-desync-any-protocol=1", "any-protocol"},
		{"--ip-id=zero", "ip-id-zero"},
		{"--dup=", "dup"},
	} {
		if strings.Contains(s, f.needle) {
			add(f.tag)
		}
	}
	if strings.Contains(s, "--dpi-desync-fake-tls-mod=") && strings.Contains(s, "sni=") {
		add("fake-sni")
	}
	return tags
}

// classify infers transport and target from the arguments alone. It stays
// deliberately coarse: a bundle's own naming ("youtube", "discord") is a claim
// about intent that the arguments do not carry, and inventing one here would put
// a recipe in a bucket nothing verified.
func classify(args []string) (strategycat.Protocol, strategycat.TargetClass) {
	s := " " + strings.Join(args, " ") + " "
	switch {
	case strings.Contains(s, "--dpi-desync-fake-quic=") || strings.Contains(s, "--filter-l7=quic"):
		return strategycat.ProtoQUIC, strategycat.ClassQUIC
	case strings.Contains(s, "--filter-udp="):
		return strategycat.ProtoUDP, strategycat.ClassOther
	default:
		return strategycat.ProtoTCP, strategycat.ClassOther
	}
}

func value(haystack, flag string) (string, bool) {
	i := strings.Index(haystack, flag)
	if i < 0 {
		return "", false
	}
	rest := haystack[i+len(flag):]
	if j := strings.IndexAny(rest, " \t"); j >= 0 {
		rest = rest[:j]
	}
	return rest, rest != ""
}

func hasAnyPrefix(s string, prefixes []string) bool {
	_, ok := prefixOf(s, prefixes)
	return ok
}

func prefixOf(s string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return p, true
		}
	}
	return "", false
}

func flagName(a string) string {
	if i := strings.IndexByte(a, '='); i >= 0 {
		return a[:i]
	}
	return a
}
