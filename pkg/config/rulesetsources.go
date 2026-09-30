package config

import (
	"sort"
	"time"

	"github.com/strace-me/lotsman/pkg/configv2"
)

// DataSourceV2raySRS is the data source type that means "a sing-box rule-set bundle
// published as a GitHub release", the shape runetfreedom ships.
const DataSourceV2raySRS = "v2ray-srs"

// RuleSetSource is one fetched rule-set bundle, resolved from a DOCUMENT rather
// than from a command-line flag.
type RuleSetSource struct {
	// Name is the data source's key, which is what a list references.
	Name string
	// Repo is the GitHub repository whose release carries the bundle. Empty for a
	// source that is only read from disk (a plain rule-set directory), which is a
	// legitimate choice: the operator already has a pipeline.
	Repo string
	// Pin is a release tag. Empty means "track latest", which is what the updater
	// does with its shrink guard in place.
	Pin string
	// Refresh is how often this source is re-read. Zero means the caller's default.
	Refresh time.Duration
	// Fetch asks for the bundle to be downloaded and swapped into Place.
	Fetch bool
	// Place is where the rule-sets live, and is the only field a read-only source
	// needs. It is supplied by the caller because the path is a deployment choice,
	// not something a config file can dictate — the same reason v1's `singbox_version`
	// decides behaviour while the binary path is a flag.
	Place string
}

// RuleSetSources reads a v2 document's data sources and returns what each one asks
// of the runtime.
//
// It exists because the alternative is a document that DECLARES its rule-set source
// and a binary that IGNORES it in favour of a flag. That arrangement is worse than
// either alone: the config says one thing, the flag says another, and the one that
// wins is whichever the operator remembered to pass. A v1 document has no
// data_sources, which is why the flag survives as the v1 path rather than being
// removed.
func RuleSetSources(cfg *configv2.Configuration) []RuleSetSource {
	if cfg == nil || len(cfg.DataSources) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.DataSources))
	for name := range cfg.DataSources {
		names = append(names, name)
	}
	// Stable order: these become periodic tasks, and a task list that reorders
	// between runs makes a log impossible to read across two boots.
	sort.Strings(names)

	out := make([]RuleSetSource, 0, len(names))
	for _, name := range names {
		src := cfg.DataSources[name]
		switch src.Type {
		case DataSourceV2raySRS:
			if src.Repo == "" {
				// A fetchable source with no repo cannot be fetched, and a source
				// that cannot be fetched is a rule-set set that will not exist after
				// a rebuild. The caller reports it; this returns it as read-only so the
				// list still resolves from whatever is on disk.
				out = append(out, RuleSetSource{Name: name})
				continue
			}
			out = append(out, RuleSetSource{Name: name, Repo: src.Repo, Pin: src.Pin,
				Refresh: src.Refresh.Value(), Fetch: true})
		default:
			// An unknown type is passed through as read-only rather than refused here:
			// validation already rejects it, and a second place making that decision
			// is a second answer to "is this config allowed".
			out = append(out, RuleSetSource{Name: name})
		}
	}
	return out
}
