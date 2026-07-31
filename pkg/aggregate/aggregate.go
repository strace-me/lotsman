// Package aggregate merges domain lists from several sources into one
// normalized, deduplicated list — the building block for custom hostlists
// (nfqws) and rule-sets. Pure logic for the merge; fetching is behind an
// interface so it is testable without network.
package aggregate

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// Result is a merged domain list plus counts for observability.
type Result struct {
	Domains  []string // sorted, deduplicated, exclusions removed
	Sources  int      // sources successfully merged
	Excluded int      // domains dropped because they were on an exclude list
	Invalid  int      // lines skipped as not a valid domain
}

// Source names where to fetch a list.
type Source struct {
	Name string
	URL  string
}

// ParseList extracts domains from raw list bytes: one per line, '#' and '//'
// comments stripped, whitespace trimmed, lowercased. Invalid lines are skipped
// and counted.
func ParseList(raw []byte) (domains []string, invalid int) {
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if !validDomain(line) {
			invalid++
			continue
		}
		domains = append(domains, line)
	}
	return domains, invalid
}

// Merge unions the lists, removes exclusions and duplicates, and returns a
// sorted result. Inputs are assumed already parsed/normalized.
func Merge(lists [][]string, exclude []string) Result {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[matchKey(e)] = true
	}
	seen := map[string]bool{}
	res := Result{Sources: len(lists)}
	for _, list := range lists {
		for _, d := range list {
			if skip[matchKey(d)] {
				res.Excluded++
				continue
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			res.Domains = append(res.Domains, d)
		}
	}
	sort.Strings(res.Domains)
	return res
}

// Manager fetches and merges sources.
type Manager struct {
	fetcher subscription.Fetcher
}

// errExcludeIncomplete marks a rebuild abandoned because an exclude source could not be
// fetched. Returning an empty Result with it makes the caller's zero-domain guard keep
// the existing file, which is exactly the desired outcome.
var errExcludeIncomplete = errors.New("aggregate: an exclude source could not be fetched — refusing to build a list that would wrongly include its domains")

// NewManager builds a Manager over a fetcher.
func NewManager(f subscription.Fetcher) *Manager {
	return &Manager{fetcher: f}
}

// Build fetches every source and excludeSource, parses them, and merges. A
// source that fails to fetch is skipped with an error returned in errs — one
// dead mirror does not sink the whole list.
//
// EXCLUDE sources are not symmetric with include sources, and treating them as if
// they were is dangerous: losing an include source SHRINKS the result, which the
// zero-domain and MinKeepRatio guards both catch, but losing an exclude source makes
// it GROW — straight past both guards and into the written file. One 503 on a
// "domains to leave alone" mirror would silently put every one of them back into the
// desync's hostlist. So a failed exclude fetch fails the whole rebuild instead, and
// the caller keeps yesterday's good list.
func (m *Manager) Build(ctx context.Context, sources, excludeSources []Source) (Result, []error) {
	var errs []error

	var invalid int
	gather := func(srcs []Source) ([][]string, bool) {
		var out [][]string
		ok := true
		for _, s := range srcs {
			raw, err := m.fetcher.Fetch(ctx, s.URL)
			if err != nil {
				errs = append(errs, &sourceError{name: s.Name, err: err})
				ok = false
				continue
			}
			domains, bad := ParseList(raw)
			invalid += bad
			out = append(out, domains)
		}
		return out, ok
	}

	includeLists, _ := gather(sources)
	excludeLists, excludeOK := gather(excludeSources)
	if !excludeOK {
		errs = append(errs, errExcludeIncomplete)
		return Result{Invalid: invalid}, errs
	}

	var exclude []string
	for _, l := range excludeLists {
		exclude = append(exclude, l...)
	}
	res := Merge(includeLists, exclude)
	res.Invalid = invalid
	return res, errs
}

// matchKey normalises a list entry for comparison. Real blocklists write the same
// domain as "foo.com", ".foo.com" and "*.foo.com"; comparing them verbatim meant an
// exclusion in one form silently matched nothing — and reported Excluded=0 as success,
// so the operator saw a healthy rebuild that had excluded none of what they asked for.
func matchKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*.")
	return strings.TrimPrefix(s, ".")
}

// validDomain is a lightweight sanity check: a dotted host with no spaces or
// schema. It is intentionally permissive (lists carry wildcards like *.foo.com
// and leading dots) but rejects obvious junk.
func validDomain(s string) bool {
	if strings.ContainsAny(s, " \t/:") {
		return false
	}
	s = strings.TrimPrefix(s, "*.")
	s = strings.TrimPrefix(s, ".")
	if !strings.Contains(s, ".") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// ValidDomain reports whether s is a plausible host/wildcard for a hostlist or
// a sing-box route match. Exported so config can validate inline service domains.
func ValidDomain(s string) bool { return validDomain(strings.ToLower(strings.TrimSpace(s))) }

// NormalizeCIDR validates s as an IP or CIDR and returns it in canonical CIDR
// form (a bare IP becomes /32 or /128). ok=false if s is neither — used to
// validate inline service IP ranges (the geoip replacement for UDP/voice routing,
// e.g. Discord voice servers, which carry no SNI for domain matching).
func NormalizeCIDR(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if _, _, err := net.ParseCIDR(s); err == nil {
		return s, true
	}
	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return s + "/32", true
		}
		return s + "/128", true
	}
	return "", false
}

// ParseIPList extracts CIDRs from raw list bytes (one per line, '#'/'//' comments
// stripped), normalizing bare IPs to /32 or /128. Invalid lines are skipped and
// counted — mirrors ParseList for IP-based aggregates.
func ParseIPList(raw []byte) (cidrs []string, invalid int) {
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if c, ok := NormalizeCIDR(line); ok {
			cidrs = append(cidrs, c)
		} else {
			invalid++
		}
	}
	return cidrs, invalid
}

type sourceError struct {
	name string
	err  error
}

func (e *sourceError) Error() string { return "aggregate source " + e.name + ": " + e.err.Error() }
