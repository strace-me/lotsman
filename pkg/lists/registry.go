package lists

import (
	"fmt"
	"sort"

	"github.com/strace-me/lotsman/pkg/rulesets"
)

// SourceConfig describes one named data source: where its compiled rule-sets
// live and which engine turns them into list entries. Two sources over the same
// loader is the whole point — russia-v2ray-rules-dat and russia-blocked-geosite
// both publish geosite categories, and a category present in one must never be
// silently served out of the other.
type SourceConfig struct {
	Name    string
	LiveDir string
	// Categories the source is expected to carry, checked up front. A category
	// missing from a source is a diagnosable error, not a hole to be filled from
	// whichever other source happens to have it.
	Categories []string
	Decompiler rulesets.EntryDecompiler
}

// Registry builds the providers for a configuration and keeps each source's
// categories separate.
type Registry struct {
	providers Providers
	sources   map[string]SourceConfig
}

func NewRegistry(sources []SourceConfig) (*Registry, error) {
	registry := &Registry{providers: Providers{}, sources: map[string]SourceConfig{}}
	for _, source := range sources {
		if source.Name == "" {
			return nil, fmt.Errorf("data source has no name")
		}
		if _, exists := registry.sources[source.Name]; exists {
			return nil, fmt.Errorf("duplicate data source %q", source.Name)
		}
		registry.sources[source.Name] = source
		registry.providers[source.Name] = SRSProvider{
			ProviderName: source.Name,
			LiveDir:      source.LiveDir,
			Decompiler:   source.Decompiler,
		}
	}
	return registry, nil
}

func (r *Registry) Providers() Providers { return r.providers }

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.sources))
	for name := range r.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Has reports whether a source declares a category. It is the check that keeps
// a geosite category from one source out of another source's lists.
func (r *Registry) Has(source, dataset, category string) bool {
	config, ok := r.sources[source]
	if !ok {
		return false
	}
	for _, declared := range config.Categories {
		if declared == dataset+":"+category {
			return true
		}
	}
	return false
}

// Resolve returns the provider for a source, refusing a source that does not
// declare the requested category. Failing here, before anything is fetched, is
// what makes a missing category an error instead of a silent empty list.
func (r *Registry) Resolve(source, dataset, category string) (Provider, error) {
	config, ok := r.sources[source]
	if !ok {
		return nil, fmt.Errorf("unknown data source %q", source)
	}
	provider, ok := r.providers[source]
	if !ok {
		return nil, fmt.Errorf("data source %q has no provider", source)
	}
	if len(config.Categories) > 0 && !r.Has(source, dataset, category) {
		return nil, fmt.Errorf("data source %q has no category %s:%s", source, dataset, category)
	}
	return provider, nil
}
