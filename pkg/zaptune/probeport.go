package zaptune

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// ProbePort is the TCP port the service's own probe target reaches, which is the
// only port this client can actually observe. 0 means "unknown" — then nothing is
// filtered, because guessing would be worse than not filtering.
func ProbePort(svc registry.Service) int {
	t := svc.ProbeTarget
	if t == "" {
		return 0
	}
	if u, err := url.Parse(t); err == nil && u.Host != "" {
		if p := u.Port(); p != "" {
			n, _ := strconv.Atoi(p)
			return n
		}
		switch u.Scheme {
		case "https":
			return 443
		case "http":
			return 80
		}
	}
	// tcp/stun probes are given as host:port.
	if _, p, ok := strings.Cut(t, ":"); ok {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	return 0
}

// recipeCoversPort reports whether a recipe's hard port filter includes port.
// A recipe with no port filter matches everything, so it covers any port.
//
// This decides what the canary is ALLOWED to judge. A recipe scoped to Discord's
// media ports cannot affect a probe of the API on 443 — no matter how good it is
// — so applying it and then recording the probe's failure against it would blame
// a strategy the measurement never touched. That is the same mistake as judging a
// strategy while the engine is down, only quieter: the KB fills up with verdicts
// about things that were never tested, and good recipes get demoted out of reach.
func recipeCoversPort(r strategycat.Recipe, port int) bool {
	if port == 0 {
		return true
	}
	filtered := false
	for _, a := range r.NfqwsArgs {
		spec, ok := strings.CutPrefix(a, "--filter-tcp=")
		if !ok {
			continue
		}
		filtered = true
		if portSpecCovers(spec, port) {
			return true
		}
	}
	return !filtered
}

// portSpecCovers parses nfqws port syntax: a comma list of ports and a-b ranges.
func portSpecCovers(spec string, port int) bool {
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			l, err1 := strconv.Atoi(lo)
			h, err2 := strconv.Atoi(hi)
			if err1 == nil && err2 == nil && port >= l && port <= h {
				return true
			}
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n == port {
			return true
		}
	}
	return false
}
