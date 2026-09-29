package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/strategystore"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// loadV2Config reads a v2 document and projects it onto the runtime Config.
//
// It reuses the v2 loader the daemon's preflight uses, so a config that generates
// here is one the daemon also accepts — a second reader would mean the CLI and the
// daemon disagree about what a config IS, which is the one class of bug a preflight
// exists to catch.
//
// The gaps list is printed rather than swallowed: the projection leaves DNS, uTLS,
// the target sing-box version and declared strategies at zero, and a config that
// generates "fine" with all four silently defaulted must say so.
func loadV2Config(configPath, rulesDir, storeRoot string) (*config.Config, error) {
	// Accept the DIRECTORY, because that is what an operator has: a v2 deployment
	// is configuration.yaml + rules/ as a unit. Being handed the file directly is
	// still fine, and a path that is neither is an error worth naming.
	if info, err := os.Stat(configPath); err == nil && info.IsDir() {
		configPath = filepath.Join(configPath, "configuration.yaml")
		rulesDir = filepath.Join(filepath.Dir(configPath), "rules")
	}
	cfg, err := configv2.Load(configPath)
	if err != nil {
		return nil, err
	}
	if rulesDir == "" {
		rulesDir = filepath.Join(filepath.Dir(configPath), "rules")
	}
	rules, err := configv2.LoadRules(rulesDir)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	// Strategy bundles resolve the {instance, strategy} references a rule makes.
	// A missing store is not fatal here: it makes every pinned strategy an unknown
	// reference, which is a real error worth reporting, but the rule set is still
	// worth printing so the operator can see the failure in context.
	var strategyErr error
	// The SAME resolver the v2 preflight uses, not a second reader of the store.
	// The store is keyed by ENGINE (strategies/<engine>/<id>), and a bundle belongs
	// to every instance running that engine.
	adapters := map[string]strategystore.EngineAdapter{
		zapret.NfqwsEngine{}.Engine():  zapret.NfqwsEngine{},
		zapret.Nfqws2Engine{}.Engine(): zapret.Nfqws2Engine{},
	}
	bundles := map[string][]string{}
	if _, err := os.Stat(storeRoot); err == nil {
		bundles = bundleStrategies(storeRoot, adapters, cfg, func(string, ...any) {})
	} else {
		strategyErr = fmt.Errorf("strategy store %s: %v", storeRoot, err)
	}
	if err := configv2.ValidateReferencesAll(cfg, rules, bundles); err != nil {
		return nil, err
	}
	projected, gaps, err := config.FromV2(cfg, rules)
	if err != nil {
		return nil, err
	}
	for _, gap := range gaps {
		fmt.Fprintf(os.Stderr, "v2 gap: %s\\n", gap)
	}
	if strategyErr != nil {
		fmt.Fprintf(os.Stderr, "v2 note: %v (pinned strategies may be unverifiable)\\n", strategyErr)
	}
	fmt.Fprintf(os.Stderr, "v2: %d rule(s) loaded, %d enabled, %d disabled\\n",
		len(rules), len(rules)-len(projected.DisabledServices), len(projected.DisabledServices))
	return projected, nil
}
