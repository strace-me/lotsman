package lists

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

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
	Kind     Kind
	Type     string
	Location string
}

type Snapshot struct {
	Name    string
	Kind    Kind
	Values  []string
	Sources int
	Invalid int
	Refs    []SourceRef
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
	if d.Source != "" {
		provided, err := providers.Load(ctx, d.Source, Reference{Kind: d.Kind, Dataset: d.Dataset, Category: d.Category}, fetcher)
		if err != nil {
			return Snapshot{}, err
		}
		raw = append(raw, provided...)
		refs = append(refs, SourceRef{Kind: d.Kind, Type: "provider", Location: d.Source + "/" + d.Dataset + "/" + d.Category})
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
		refs = append(refs, SourceRef{Kind: d.Kind, Type: "file", Location: file})
	}
	if len(d.URLs) > 0 && fetcher == nil {
		return Snapshot{}, fmt.Errorf("list %q has URL sources but no fetcher", d.Name)
	}
	if fetcher != nil {
		for _, url := range d.URLs {
			data, err := fetcher.Fetch(ctx, url)
			if err != nil {
				return Snapshot{}, err
			}
			raw = append(raw, data)
			refs = append(refs, SourceRef{Kind: d.Kind, Type: "url", Location: url})
		}
	}
	var parsed [][]string
	invalid := 0
	for _, data := range raw {
		switch d.Kind {
		case Domains:
			values, bad := aggregate.ParseList(data)
			parsed = append(parsed, values)
			invalid += bad
		case IPs:
			values, bad := aggregate.ParseIPList(data)
			parsed = append(parsed, values)
			invalid += bad
		}
	}
	values := merge(d.Kind, parsed)
	return Snapshot{Name: d.Name, Kind: d.Kind, Values: values, Sources: len(raw), Invalid: invalid, Refs: refs}, nil
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
