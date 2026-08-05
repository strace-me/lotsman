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
	"--hostlist-exclude=", "--hostlist-exclude-domains=", "--ipset-exclude=",
	"--hostlist-auto=", "--hostlist-auto-fail-threshold=",
}

// Flags that answer "which hosts is this pointed at". Bundles express it either
// as a file they ship or as an inline list; both are the same decision, and it
// is a decision Lotsman makes per service from the routing model. Collapsing
// both spellings to one placeholder is what lets the Windows and Linux editions
// of a strategy be recognised as the same recipe.
var hostSelectors = []string{"--hostlist=", "--hostlist-domains="}

const domainsPlaceholder = "--hostlist-domains={{DOMAINS}}"

// Arguments selecting traffic by IP set. Unlike an exclusion these are the
// block's POSITIVE target, and Lotsman has no IP set to point them at. They
// become a placeholder rather than being dropped or refused: the curated catalog
// spells them the same way, and `zaptune.recipeRenderable` already refuses to
// render any recipe carrying a placeholder it cannot fill. One policy, in the
// place that already had it, instead of a second one here.
var ipsetSelectors = []string{"--ipset=", "--ipset-auto="}

const ipsetPlaceholder = "--ipset={{IPSET}}"

// The desync vocabulary we actually model, gathered from the 67 curated recipes
// and a real Flowseal 1.10.0 bundle — 30 distinct flags between them. An
// argument outside this set means a bundle doing something we have never seen,
// and the recipe is skipped with the offending flag named.
//
// The alternative, letting an unknown flag through, is worse than it looks. If
// nfqws rejects it the engine dies at startup and takes the whole composed
// config with it — every OTHER service's block in the same process. If nfqws
// ACCEPTS it, we are running a strategy whose behaviour we cannot describe and
// whose outcome the KB will record as if we could.
//
// Keeping this list current is a one-line edit prompted by a skip reason, which
// is the point: a new upstream flag should surface as a question, not as an
// engine that will not start.
var knownFlags = map[string]bool{
	"--dpi-desync": true, "--dpi-desync-any-protocol": true, "--dpi-desync-autottl": true,
	"--dpi-desync-badseq-increment": true, "--dpi-desync-cutoff": true,
	"--dpi-desync-fakedsplit-pattern": true, "--dpi-desync-fooling": true,
	"--dpi-desync-hostfakesplit-midhost": true, "--dpi-desync-hostfakesplit-mod": true,
	"--dpi-desync-repeats": true, "--dpi-desync-split-pos": true,
	"--dpi-desync-split-seqovl": true, "--dpi-desync-split-seqovl-pattern": true,
	"--dpi-desync-ttl": true, "--dpi-desync-start": true,
	"--dup": true, "--dup-cutoff": true, "--dup-fooling": true,
	"--filter-l3": true, "--filter-l7": true, "--filter-tcp": true, "--filter-udp": true,
	"--hostlist-domains": true, "--ipset": true, "--ip-id": true, "--mss": true,
}

// Families whose members share one meaning, so a member we have not met is still
// one we understand. `--dpi-desync-fake-<proto>=<file>.bin` names a payload for a
// protocol; the protocol list grows with every nfqws release and the semantics do
// not. `--orig-*` likewise adjusts the original packet.
var knownFlagPrefixes = []string{"--dpi-desync-fake-", "--orig-"}

// A reference the extractor could not resolve — shell $NAME or batch %NAME%.
// %~dp0 is not one of these: it means "the directory holding me", which the
// basename reduction discards on purpose.
var unresolvedVar = regexp.MustCompile(`\$\{?[A-Za-z_]|%[A-Za-z_][A-Za-z0-9_]*%`)

// Normalize turns one block's raw arguments into the canonical form the catalog
// stores: deployment-specific arguments removed, payload paths reduced to the
// bare filename, and the target host set replaced by a placeholder Lotsman fills
// per service. It returns an error when the block cannot be represented.
func Normalize(raw []string) ([]string, error) {
	var out []string
	for _, a := range raw {
		a = strings.Trim(a, `"'`)
		// Batch quotes the VALUE, not the token: --flag="%BIN%x.bin". Trimming
		// the token alone leaves the quote sitting inside, where it becomes part
		// of a filename and silently makes two spellings of one recipe.
		if k, v, ok := strings.Cut(a, "="); ok {
			a = k + "=" + strings.Trim(v, `"'`)
		}
		if a == "" || dropExact[a] {
			continue
		}
		if hasAnyPrefix(a, ipsetSelectors) {
			if len(out) == 0 || out[len(out)-1] != ipsetPlaceholder {
				out = append(out, ipsetPlaceholder)
			}
			continue
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
		if name := flagName(a); !knownFlags[name] && !hasAnyPrefix(name, knownFlagPrefixes) {
			return nil, fmt.Errorf("unknown flag %s — this bundle does something we do not model", name)
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
