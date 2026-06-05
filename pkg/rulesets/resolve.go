package rulesets

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// resolve.go closes the coherence gap (TM-1b): it turns a sing-box rule_set tag
// (e.g. "geosite-discord") into the plaintext domains that the .srs blob carries,
// so the zapret composer can feed a service's FULL domain set to the nfqws
// hostlist — not just its inline domains. Without this, arming the composer
// regressed discord (discord.com lives in geosite-discord, invisible to nfqws).
//
// The zaptune.Resolver contract is "(domains, ok) where ok=false means the gate
// keeps the service UNCOMPOSED". So every failure path here returns (nil, false):
// a missing mapping, an absent/unreadable .srs, or a decompile error all leave
// the service on its working strategy rather than composing an incomplete (and
// thus regressing) hostlist.

// Decompiler turns a .srs file into the plaintext match domains it carries.
// Injected so the resolver is unit-testable without the sing-box binary.
type Decompiler interface {
	Domains(srsPath string) ([]string, error)
}

// SingboxDecompiler extracts domains from a .srs via `sing-box rule-set decompile`.
// Only `domain` (exact) and `domain_suffix` matches are returned — those are the
// plaintext hostnames nfqws can match. `domain_keyword`/`domain_regex`/`ip_cidr`
// are dropped (nfqws's hostlist cannot express them).
type SingboxDecompiler struct {
	Bin string // sing-box binary ("" => "sing-box")
}

// Domains decompiles the .srs to JSON and collects its domain + domain_suffix
// entries (lower-cased, leading dot stripped, deduped, sorted).
func (d SingboxDecompiler) Domains(srsPath string) ([]string, error) {
	bin := d.Bin
	if bin == "" {
		bin = "sing-box"
	}
	tmp, err := os.CreateTemp("", "lotsman-srs-resolve-*.json")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "rule-set", "decompile", srsPath, "-o", tmp.Name()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("decompile %s: %w: %s", srsPath, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	var doc struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse decompiled %s: %w", srsPath, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, rule := range doc.Rules {
		for _, k := range []string{"domain", "domain_suffix"} {
			raw, ok := rule[k]
			if !ok {
				continue
			}
			var arr []string
			if json.Unmarshal(raw, &arr) != nil {
				continue
			}
			for _, dom := range arr {
				dom = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(dom, "*.")))
				dom = strings.TrimPrefix(dom, ".")
				if dom == "" || seen[dom] {
					continue
				}
				seen[dom] = true
				out = append(out, dom)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Resolver implements the zaptune.Resolver contract: tag -> (plaintext domains,
// ok). It maps the tag to its .srs (PathFor under LiveDir), decompiles it, and
// caches the result keyed by the file's mtime — so a rule-set autoupdate that
// swaps the .srs is picked up automatically on the next resolve, with no restart.
type Resolver struct {
	LiveDir    string     // rule-set root (== singbox RuleSetDir, e.g. /etc/sing-box)
	PathFor    PathFor    // tag -> relative .srs path (singbox.RuleSetRelPath)
	Decompiler Decompiler // "" Bin SingboxDecompiler in prod
	Log        *slog.Logger

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	mtime   time.Time
	domains []string
}

// Resolve is the zaptune.Resolver-shaped method. Any failure returns (nil, false)
// so the composer's coherence gate keeps the service uncomposed (safe default).
func (r *Resolver) Resolve(tag string) ([]string, bool) {
	rel, ok := r.PathFor(tag)
	if !ok {
		r.log().Debug("rulesets-resolve: no path mapping for tag", "tag", tag)
		return nil, false
	}
	path := filepath.Join(r.LiveDir, rel)
	fi, err := os.Stat(path)
	if err != nil {
		r.log().Warn("rulesets-resolve: .srs not found, leaving service uncomposed", "tag", tag, "path", path, "err", err)
		return nil, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]cacheEntry{}
	}
	if e, ok := r.cache[tag]; ok && e.mtime.Equal(fi.ModTime()) {
		return e.domains, true
	}

	domains, err := r.Decompiler.Domains(path)
	if err != nil {
		r.log().Warn("rulesets-resolve: decompile failed, leaving service uncomposed", "tag", tag, "err", err)
		return nil, false
	}
	r.cache[tag] = cacheEntry{mtime: fi.ModTime(), domains: domains}
	r.log().Info("rulesets-resolve: resolved rule_set to plaintext domains", "tag", tag, "domains", len(domains))
	return domains, true
}

func (r *Resolver) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
