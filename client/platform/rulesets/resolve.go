package rulesets

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// decompiled is the shape `sing-box rule-set decompile` emits.
type decompiled struct {
	Rules []struct {
		Domain       []string `json:"domain"`
		DomainSuffix []string `json:"domain_suffix"`
	} `json:"rules"`
}

// ParseDecompiled pulls the plaintext domains out of a decompiled rule-set.
// Split from the exec call so it is unit-testable. A geoip rule-set legitimately
// yields none — it carries ip_cidr, and nfqws matches by hostname, so there is
// simply nothing for the desync hostlist to take from it.
func ParseDecompiled(data []byte) ([]string, error) {
	var d decompiled
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("rulesets: parse decompiled: %w", err)
	}
	var out []string
	for _, r := range d.Rules {
		out = append(out, r.Domain...)
		out = append(out, r.DomainSuffix...)
	}
	return out, nil
}

// Resolver resolves a rule-set tag to its plaintext domains by decompiling the
// provisioned .srs with sing-box itself. This is what makes the desync COHERENT
// with the routing: nfqws then covers the same domains sing-box routes, instead
// of only a service's inline domains (discord's gateway, for instance, lives in
// geosite-discord — missing it silently breaks the service on the zapret rung).
//
// Results are cached: a rule-set is decompiled at most once per process.
type Resolver struct {
	bin string // sing-box binary
	dir string // rule-set dir
	log *slog.Logger

	mu    sync.Mutex
	cache map[string][]string
}

// NewResolver returns a Resolver reading the .srs files under dir.
func NewResolver(singboxBin, dir string, log *slog.Logger) *Resolver {
	if singboxBin == "" {
		singboxBin = "sing-box"
	}
	return &Resolver{bin: singboxBin, dir: dir, log: log, cache: map[string][]string{}}
}

// Resolve implements zaptune.Resolver. ok=false means the tag could not be
// resolved at all — the caller must then NOT compose that service, because
// desyncing a partial domain set is worse than not desyncing it.
func (r *Resolver) Resolve(tag string) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.cache[tag]; ok {
		return d, true
	}
	rel, ok := RelPath(tag)
	if !ok {
		return nil, false
	}
	src := filepath.Join(r.dir, filepath.FromSlash(rel))
	if _, err := os.Stat(src); err != nil {
		r.log.Warn("rule-set not provisioned, cannot resolve domains", "tag", tag, "path", src)
		return nil, false
	}

	tmp, err := os.CreateTemp("", "lotsman-ruleset-*.json")
	if err != nil {
		r.log.Warn("rule-set decompile: temp file", "tag", tag, "err", err)
		return nil, false
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.CommandContext(context.Background(), r.bin, "rule-set", "decompile", src, "-o", tmp.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		r.log.Warn("rule-set decompile failed", "tag", tag, "err", err, "out", string(out))
		return nil, false
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		r.log.Warn("rule-set decompile: read", "tag", tag, "err", err)
		return nil, false
	}
	domains, err := ParseDecompiled(data)
	if err != nil {
		r.log.Warn("rule-set decompile: parse", "tag", tag, "err", err)
		return nil, false
	}
	r.cache[tag] = domains
	return domains, true
}
