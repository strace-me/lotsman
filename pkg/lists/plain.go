package lists

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/subscription"
	"gopkg.in/yaml.v3"
)

// PlainProvider serves categories from a sing-box PLAIN rule-set file — the
// `lists: [{name, rules: ["domain:example.com", ...]}]` shape that ships next to
// a compiled geosite/geoip database.
//
// It exists because the binary .dat is not an option on this box: sing-box
// 1.13.14 refuses it ("open geosite file: unknown version" — it wants its own
// .db), and the v2ray->srs converter is a separate binary that is not
// installed. The plain form carries the same categories as text, so it needs no
// converter at all.
//
// Rules a hostlist cannot express (regexp:, keyword:) are NOT silently folded
// into the list: they are counted and reported, because a category that quietly
// loses 387 regex rules looks identical to one that never had them.
type PlainProvider struct {
	ProviderName string
	// Path is the plain rule-set file this source publishes.
	Path string

	mu      sync.Mutex
	index   map[string][]string
	indexed time.Time
	byPath  string
	parses  int // guarded by mu; a regression guard on the index actually being used
}

// The index lives on the pointer, deliberately: with a value receiver every Load
// would work on a copy, so the cache would never be hit and the mutex would guard
// nothing. Measured on the real 3 MB community file: 370 ms per category instead
// of ~0 for the second one.
func (p *PlainProvider) Name() string { return p.ProviderName }

func (p *PlainProvider) Load(_ context.Context, ref Reference, _ subscription.Fetcher) (Payload, error) {
	categories, err := p.categories()
	if err != nil {
		return Payload{}, err
	}
	rules, ok := categories[ref.Category]
	if !ok {
		return Payload{}, fmt.Errorf("data source %q has no category %q", p.ProviderName, ref.Category)
	}
	values, skipped := selectKind(rules, ref.Kind)
	return Payload{
		Data:      [][]byte{[]byte(strings.Join(values, "\n"))},
		UpdatedAt: p.stamp(),
		Skipped:   skipped,
	}, nil
}

// categories indexes the file once per mtime. A plain geosite file is ~3 MB of
// YAML; re-parsing it per requested category would tax a small device for
// nothing.
func (p *PlainProvider) categories() (map[string][]string, error) {
	info, err := os.Stat(p.Path)
	if err != nil {
		return nil, fmt.Errorf("data source %q: %w", p.ProviderName, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.index != nil && p.byPath == p.Path && p.indexed.Equal(info.ModTime()) {
		return p.index, nil
	}
	parsed, err := parsePlainFile(p.Path)
	if err != nil {
		return nil, err
	}
	p.parses++
	p.index, p.byPath, p.indexed = parsed, p.Path, info.ModTime()
	return parsed, nil
}

func (p *PlainProvider) stamp() time.Time {
	info, err := os.Stat(p.Path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func parsePlainFile(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Lists []struct {
			Name  string   `yaml:"name"`
			Rules []string `yaml:"rules"`
		} `yaml:"lists"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse plain rule-set %s: %w", path, err)
	}
	out := make(map[string][]string, len(doc.Lists))
	for _, list := range doc.Lists {
		out[list.Name] = list.Rules
	}
	return out, nil
}

// selectKind splits one category's rules into the entries the requested kind can
// carry, and reports how many were dropped as inexpressible.
func selectKind(rules []string, kind Kind) (values []string, skipped int) {
	for _, rule := range rules {
		prefix, value, ok := strings.Cut(rule, ":")
		if !ok {
			skipped++
			continue
		}
		switch kind {
		case Domains:
			// domain: is a suffix match, which is what a hostlist does with a bare
			// domain, so the two agree. full: is exact and is kept as-is.
			if prefix != "domain" && prefix != "full" {
				skipped++
				continue
			}
		case IPs:
			if prefix != "ip_cidr" {
				skipped++
				continue
			}
		default:
			skipped++
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values, skipped
}
