package lists

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/subscription"
)

type Kind string

const (
	Domains Kind = "domains"
	IPs     Kind = "ips"
)

type Definition struct {
	Name     string
	Kind     Kind
	Files    []string
	URLs     []string
	Domains  []string
	CIDRs    []string
	Source   string
	Dataset  string
	Category string
}

func (d Definition) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("list has no name")
	}
	if d.Kind != Domains && d.Kind != IPs {
		return fmt.Errorf("list %q has unknown kind %q", d.Name, d.Kind)
	}
	if d.Source == "" && len(d.Files) == 0 && len(d.URLs) == 0 && len(d.Domains) == 0 && len(d.CIDRs) == 0 {
		return fmt.Errorf("list %q has no sources", d.Name)
	}
	return nil
}

type SourceRef struct {
	Kind      Kind
	Type      string
	Location  string
	UpdatedAt time.Time
}

type Snapshot struct {
	Name      string
	Kind      Kind
	Values    []string
	Sources   int
	Invalid   int
	Refs      []SourceRef
	UpdatedAt time.Time
}

func (d Definition) Load(ctx context.Context, fetcher subscription.Fetcher) (Snapshot, error) {
	return d.LoadWith(ctx, nil, fetcher)
}

func (d Definition) LoadWith(ctx context.Context, providers Providers, fetcher subscription.Fetcher) (Snapshot, error) {
	if err := d.Validate(); err != nil {
		return Snapshot{}, err
	}
	raw := make([][]byte, 0, len(d.Files)+len(d.URLs)+1)
	refs := make([]SourceRef, 0, len(d.Files)+len(d.URLs)+2)
	var updated time.Time
	if d.Source != "" {
		payload, err := providers.Load(ctx, d.Source, Reference{Kind: d.Kind, Dataset: d.Dataset, Category: d.Category}, fetcher)
		if err != nil {
			return Snapshot{}, err
		}
		raw = append(raw, payload.Data...)
		refs = append(refs, SourceRef{Kind: d.Kind, Type: "provider", Location: d.Source + "/" + d.Dataset + "/" + d.Category, UpdatedAt: payload.UpdatedAt})
		updated = laterOf(updated, payload.UpdatedAt)
	}
	if len(d.Domains) > 0 || len(d.CIDRs) > 0 {
		var values []string
		switch d.Kind {
		case Domains:
			values = append(values, d.Domains...)
		case IPs:
			values = append(values, d.CIDRs...)
		}
		raw = append(raw, []byte(strings.Join(values, "\n")))
		refs = append(refs, SourceRef{Kind: d.Kind, Type: "inline", Location: d.Name})
	}
	for _, file := range d.Files {
		data, err := os.ReadFile(file)
		if err != nil {
			return Snapshot{}, err
		}
		raw = append(raw, data)
		modified := time.Time{}
		if info, err := os.Stat(file); err == nil {
			modified = info.ModTime()
		}
		refs = append(refs, SourceRef{Kind: d.Kind, Type: "file", Location: file, UpdatedAt: modified})
		updated = laterOf(updated, modified)
	}
	if len(d.URLs) > 0 && fetcher == nil {
		return Snapshot{}, fmt.Errorf("list %q has URL sources but no fetcher", d.Name)
	}
	if fetcher != nil {
		fetched := time.Now()
		for _, url := range d.URLs {
			data, err := fetcher.Fetch(ctx, url)
			if err != nil {
				return Snapshot{}, err
			}
			raw = append(raw, data)
			refs = append(refs, SourceRef{Kind: d.Kind, Type: "url", Location: url, UpdatedAt: fetched})
			updated = laterOf(updated, fetched)
		}
	}
	var parsed [][]string
	invalid := 0
	for _, data := range raw {
		values, bad := ParseValues(d.Kind, data)
		parsed = append(parsed, values)
		invalid += bad
	}
	values := merge(d.Kind, parsed)
	return Snapshot{Name: d.Name, Kind: d.Kind, Values: values, Sources: len(raw), Invalid: invalid, Refs: refs, UpdatedAt: updated}, nil
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Stale reports whether the snapshot's newest data is older than the refresh
// interval. A zero UpdatedAt (pure inline list) is never stale.
func (s Snapshot) Stale(interval time.Duration) bool {
	if interval <= 0 || s.UpdatedAt.IsZero() {
		return false
	}
	return time.Since(s.UpdatedAt) > interval
}

// ParseValues parses one raw source into entries of the given kind. It is the
// single parse path: providers, files and URLs all go through it, so a rule
// spelled the same way means the same thing wherever it came from.
func ParseValues(kind Kind, data []byte) ([]string, int) {
	switch kind {
	case Domains:
		return aggregate.ParseList(data)
	case IPs:
		return aggregate.ParseIPList(data)
	}
	return nil, 0
}

func merge(kind Kind, lists [][]string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, list := range lists {
		for _, value := range list {
			if seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	if kind == Domains {
		return out
	}
	return out
}
