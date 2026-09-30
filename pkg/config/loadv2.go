package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/strategystore"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// V2Load is the resolved inputs of a v2 document on disk.
type V2Load struct {
	Config  *configv2.Configuration
	Rules   []configv2.Rule
	Bundles map[string][]string // desync instance name -> strategy ids its store holds
	// StoreMissing is set when the bundle root does not exist. It is NOT an error:
	// an operator can legitimately have no bundles yet, and a document whose rungs
	// are all `strategy: auto` needs none. The caller decides how loudly to say so,
	// because a pinned strategy that cannot be resolved is a different fact.
	StoreMissing error
}

// LoadV2 reads a v2 document and everything it references, and validates it.
//
// It lives here, not in either root, because the CLI and the daemon must agree on
// what a v2 configuration IS. A second reader would mean a config that validates
// in `lotsmanctl` and is rejected by `lotsmand` — the one class of bug a preflight
// exists to catch, which is also why `pkg/config` is the right home: it already
// imports configv2 for the migration, and putting this on the other side would
// close a cycle.
//
// configPath may be the DIRECTORY or the configuration.yaml inside it, because a
// v2 deployment is the two as a unit and an operator has the directory.
func LoadV2(configPath, rulesDir, storeRoot string) (*V2Load, error) {
	if info, err := os.Stat(configPath); err == nil && info.IsDir() {
		rulesDir = filepath.Join(configPath, "rules")
		configPath = filepath.Join(configPath, "configuration.yaml")
	}
	if rulesDir == "" {
		rulesDir = filepath.Join(filepath.Dir(configPath), "rules")
	}
	cfg, err := configv2.Load(configPath)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rules, err := configv2.LoadRules(rulesDir)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}

	out := &V2Load{Config: cfg, Rules: rules}
	// The store is keyed by ENGINE (strategies/<engine>/<id>) and a bundle belongs to
	// every instance running that engine, so the adapters decide which directories
	// to look in. Matching by the INSTANCE name instead would resolve nothing on a
	// real install.
	adapters := map[string]strategystore.EngineAdapter{
		zapret.NfqwsEngine{}.Engine():  zapret.NfqwsEngine{},
		zapret.Nfqws2Engine{}.Engine(): zapret.Nfqws2Engine{},
	}
	if _, err := os.Stat(storeRoot); err != nil {
		out.StoreMissing = fmt.Errorf("strategy store %s: %w", storeRoot, err)
		out.Bundles = map[string][]string{}
	} else {
		out.Bundles = BundleStrategies(storeRoot, adapters, cfg)
	}
	if err := configv2.ValidateReferencesAll(cfg, rules, out.Bundles); err != nil {
		return nil, err
	}
	// A detour cycle is a WHOLE-DOCUMENT property: each rule names one detour, so
	// the ring only closes across rules. Checking it per rule — where the reference
	// validation lives — would pass a document that can never resolve a path.
	if err := configv2.ValidateDetours(rules); err != nil {
		return nil, err
	}
	return out, nil
}

// BundleStrategies lists the strategy ids each desync instance's store holds.
func BundleStrategies(root string, adapters map[string]strategystore.EngineAdapter, cfg *configv2.Configuration) map[string][]string {
	out := map[string][]string{}
	for name, instance := range cfg.Desync.Instances {
		adapter, ok := adapters[instance.Engine]
		if !ok {
			continue
		}
		// The store holds every bundle under its ENGINE directory; an instance
		// running that engine sees all of them, which is what lets two instances of
		// different engines share one store on disk.
		bundles, err := strategystore.LoadDir(filepath.Join(root, adapter.Engine()))
		if err != nil {
			continue
		}
		var ids []string
		for _, b := range bundles {
			if b.Manifest.ID != "" {
				ids = append(ids, b.Manifest.ID)
			}
		}
		sort.Strings(ids)
		out[name] = ids
	}
	return out
}

// EnabledRules returns the rules that are on, and the names of the ones that are
// off. A disabled rule is absent from the runtime entirely — nothing probes,
// routes or desyncs it — and its name is carried separately so a UI can list it and
// offer to switch it back on.
func EnabledRules(rules []configv2.Rule) (on []configv2.Rule, off []string) {
	for _, r := range rules {
		if r.Enabled {
			on = append(on, r)
		} else {
			off = append(off, r.ID)
		}
	}
	sort.Strings(off)
	return on, off
}
