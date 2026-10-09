package configv2

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategystore"
)

// Hostlist declares a merged domain list to (re)build periodically: fetch every
// source, drop everything on an exclude source, merge the operator's own entries,
// dedup, and write to Out — the file nfqws reads.
//
// It is a SEPARATE section from `lists` on purpose, because the two feed different
// consumers. A `lists.domains` entry becomes a sing-box rule-set tag that sing-box
// matches; a hostlist becomes a plain text file that nfqws re-reads on mtime, which
// is why membership can change without restarting the desync engine. A v2 list
// substitutes for a hostlist on the ROUTING side and cannot substitute on the
// DESYNC side at all, because nothing about a rule-set tag produces a file nfqws
// will read.
type Hostlist struct {
	Name string `yaml:"name"`
	Out  string `yaml:"out"`
	// Sources are fetched and merged; Exclude are fetched and subtracted.
	Sources []string `yaml:"sources"`
	Exclude []string `yaml:"exclude"`
	// Domains are the operator's own entries, merged with whatever the sources
	// yield. A pack may be nothing BUT these — until they existed the only way to
	// declare your own reusable list was to publish it somewhere and fetch it back.
	Domains []string `yaml:"domains"`
	// MinKeepRatio refuses a rebuild below this fraction of the last good count. It
	// exists because a mirror that answers 200 OK with an empty body is not a
	// failure anyone would notice, and the symptom is that a service silently stops
	// being desynced while every status read stays green.
	MinKeepRatio float64 `yaml:"min_keep_ratio"`
}

// ResolvedHostlist is the validated form BuildHostlist returns: the sources named,
// so a merge error can say which URL produced the junk.
type ResolvedHostlist struct {
	Name         string
	Out          string
	Sources      []aggregate.Source
	Exclude      []aggregate.Source
	Domains      []string
	MinKeepRatio float64
}

// BuildHostlist validates one declaration.
func BuildHostlist(h Hostlist) (*ResolvedHostlist, error) {
	if h.Name == "" {
		return nil, fmt.Errorf("config: hostlist with empty name")
	}
	if h.Out == "" {
		return nil, fmt.Errorf("config: hostlist %q has no out path", h.Name)
	}
	// A list needs SOMETHING to be made of, but that something may be entirely
	// hand-written: a pack of four domains does not deserve a URL to be published at
	// and fetched back.
	if len(h.Sources) == 0 && len(h.Domains) == 0 {
		return nil, fmt.Errorf("config: hostlist %q has neither sources nor domains", h.Name)
	}
	for _, d := range h.Domains {
		if !aggregate.ValidDomain(d) {
			return nil, fmt.Errorf("config: hostlist %q: invalid domain %q", h.Name, d)
		}
	}
	if h.MinKeepRatio < 0 || h.MinKeepRatio > 1 {
		return nil, fmt.Errorf("config: hostlist %q: min_keep_ratio %v out of [0,1]", h.Name, h.MinKeepRatio)
	}
	return &ResolvedHostlist{
		Name:         h.Name,
		Out:          h.Out,
		Sources:      toSources(h.Sources),
		Exclude:      toSources(h.Exclude),
		Domains:      h.Domains,
		MinKeepRatio: h.MinKeepRatio,
	}, nil
}

// BuildHostlists validates the whole section. A duplicate name is refused: rules
// attach a list BY NAME, so a duplicate silently resolves last-wins and the
// shadowed list is fetched forever with nothing reading it.
func BuildHostlists(in []Hostlist) ([]ResolvedHostlist, error) {
	out := make([]ResolvedHostlist, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, h := range in {
		if seen[h.Name] {
			return nil, fmt.Errorf("config: duplicate hostlist %q", h.Name)
		}
		seen[h.Name] = true
		built, err := BuildHostlist(h)
		if err != nil {
			return nil, err
		}
		out = append(out, *built)
	}
	return out, nil
}

// toSources names each source by its URL for traceability in merge errors.
func toSources(urls []string) []aggregate.Source {
	s := make([]aggregate.Source, 0, len(urls))
	for _, u := range urls {
		s = append(s, aggregate.Source{Name: u, URL: u})
	}
	return s
}

// MergePack is the shared read of a pack's entries: the operator's own entries from
// the DECLARATION plus whatever the built file holds. It is a function rather than a
// one-time merge because the file is a CACHE the rebuild job owns — absent on a fresh
// install, unreadable if a privileged run wrote it — and an attached rule must degrade
// to its inline entries rather than fail to load.
//
// The operator's entries go through ParseList too, so a hand-typed "YouTube.com" is
// lowercased the way a fetched one is and reaches the generator in a case nothing else
// uses.
func MergePack(hl ResolvedHostlist) []string {
	domains, _ := aggregate.ParseList([]byte(strings.Join(hl.Domains, "\n")))
	if data, err := os.ReadFile(hl.Out); err == nil {
		fetched, _ := aggregate.ParseList(data)
		domains = append(domains, fetched...)
	}
	return domains
}

// Strategy is a desync recipe declared in the document.
//
// It exists alongside the on-disk bundle store because the two answer different
// questions. A bundle carries a third party's recipe with its own profile order and
// payload files, and it is UPDATED by replacing the bundle. This is the operator's own
// recipe, in the same file as the rule that uses it, so a strategy can be read,
// reviewed and changed with the rule in one place.
type Strategy struct {
	ID    string `yaml:"id"`
	Class string `yaml:"class"`
	// NFQWSArgs is the argv for a SINGLE-profile recipe. A bundle needing several
	// profiles belongs in the store, not here.
	NFQWSArgs  []string `yaml:"nfqws_args"`
	BlockTypes []string `yaml:"block_types"`
	Notes      string   `yaml:"notes"`
}

// BuildStrategies validates the declared strategies into the runtime definitions the
// catalog merges on top of its builtins.
func BuildStrategies(in []Strategy) ([]strategy.Definition, error) {
	out := make([]strategy.Definition, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if s.ID == "" {
			return nil, fmt.Errorf("config: strategy with empty id")
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("config: duplicate strategy %q", s.ID)
		}
		seen[s.ID] = true
		switch s.Class {
		case strategy.ClassZapret, strategy.ClassVPN, strategy.ClassEmergency, strategy.ClassDirect:
		default:
			return nil, fmt.Errorf("config: strategy %q has unknown class %q (want %s/%s/%s/%s)",
				s.ID, s.Class, strategy.ClassZapret, strategy.ClassVPN, strategy.ClassEmergency, strategy.ClassDirect)
		}
		out = append(out, strategy.Definition{
			ID:         s.ID,
			Class:      s.Class,
			NFQWSArgs:  s.NFQWSArgs,
			BlockTypes: s.BlockTypes,
			Notes:      s.Notes,
		})
	}
	return out, nil
}

// Override runs one upstream bundle VERBATIM for every rule, instead of composing a
// recipe per rule.
//
// It is a mode and not a shortcut, and the reason is a lossy operation nothing
// downstream can repair. Flowseal's ALT12 is one nfqws launch whose profiles carry
// DIFFERENT hostlists — a narrow tcp/443 treatment against the Google list, a broad one
// against the general list. We compose per RULE, one hostlist per rule, so both
// profiles would receive the same list, the second could never match, and the recipe
// would be refused as self-shadowing. The refusal is correct about what we would have
// rendered and says nothing about ALT12, which is a whole-machine bundle and simply is
// not a per-rule strategy.
//
// What the mode costs, stated plainly: per-rule recipe learning stops, because one
// bundle serves every rule and there is nothing left to choose between. The rungs still
// work — a rule on its desync rung routes direct and meets the override, a rule on VPN
// is tunnelled and never reaches it — and the canary still judges each rule on its own
// volume, so an override that stops carrying still escalates the rules it was carrying.
type Override struct {
	Name     string `yaml:"name"`
	ArgsFile string `yaml:"args_file"`
	// Dir loads EVERY *.args in a directory, so a bundle of twenty recipes becomes
	// twenty candidates without twenty declarations. The name comes from the header
	// line the converter writes, or from the filename.
	Dir string `yaml:"dir"`
}

// ResolvedOverride is the validated argv form.
type ResolvedOverride struct {
	Name string
	Args []string
}

// BuildOverride loads verbatim argv from disk — one named file, or every *.args in a
// directory.
//
// It REFUSES an empty or missing file rather than falling back to composing. Silently
// composing when the operator asked for an override is the precondition that disables a
// feature while its declaration says it is on — and here it would also be invisible,
// because a composed strategy and an override both look like "the engine is running".
func BuildOverride(o Override) ([]ResolvedOverride, error) {
	var files []string
	switch {
	case o.Dir != "":
		matches, err := filepath.Glob(filepath.Join(o.Dir, "*.args"))
		if err != nil {
			return nil, fmt.Errorf("desync override: %w", err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("desync override: %s holds no .args files", o.Dir)
		}
		sort.Strings(matches)
		files = matches
	case o.ArgsFile != "":
		files = []string{o.ArgsFile}
	default:
		return nil, nil
	}
	out := make([]ResolvedOverride, 0, len(files))
	for _, f := range files {
		parsed, err := strategystore.ReadArgs(f, o.Name, len(files) == 1)
		if err != nil {
			return nil, fmt.Errorf("desync override: %w", err)
		}
		out = append(out, ResolvedOverride{Name: parsed.Name, Args: parsed.Values})
	}
	return out, nil
}
