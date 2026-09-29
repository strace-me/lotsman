package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/lists"
	"github.com/strace-me/lotsman/pkg/rulesets"
	"github.com/strace-me/lotsman/pkg/strategystore"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// v2Flags registers the v2 preflight switch set. It is separate from the flag
// block above because it is a DIFFERENT JOB: -config runs the v1 daemon, and
// -config-v2 only reads and reports.
type v2Flags struct {
	config   *string
	rules    *string
	store    *string
	rulesets *string
	plain    *string
}

func registerV2Flags(fs *flag.FlagSet) v2Flags {
	return v2Flags{
		config: fs.String("config-v2", "", "validate a v2 configuration.yaml and its rules/, then EXIT without applying anything"),
		rules:  fs.String("rules-v2", "", "rule files dir (default: alongside the v2 config)"),
		store:  fs.String("store-v2", "/etc/lotsman/desync/strategies", "strategy bundle root, for resolving a rule's {instance, strategy}"),
		rulesets: fs.String("rulesets-v2", "",
			"compiled .srs root; enables provider-backed lists"),
		plain: fs.String("plain-v2", "",
			"plain rule-set file; enables provider-backed lists without a converter"),
	}
}

// runV2Preflight is the daemon's own reader of a v2 config, in dry-run by
// construction: it opens no socket, binds no queue, installs no rule and starts no
// engine, and the process exits when the report is printed.
//
// It exists because the preflight must go through the REAL daemon's paths, not only
// through lotsmanctl: a document that validates in the CLI and not here means the
// CLI and the daemon disagree about what a config is, which is the one class of bug
// a preflight exists to catch.
func runV2Preflight(f v2Flags) int {
	if *f.config == "" {
		return 0 // not requested; the daemon carries on
	}
	rulesDir := *f.rules
	if rulesDir == "" {
		rulesDir = configv2.RulesDir
		if filepath.Dir(*f.config) != "/" {
			candidate := filepath.Join(filepath.Dir(*f.config), "rules")
			if _, err := os.Stat(candidate); err == nil {
				rulesDir = candidate
			}
		}
	}

	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	cfg, err := configv2.Load(*f.config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lotsmand -config-v2: %v\n", err)
		return 1
	}
	fmt.Printf("configuration %s: version %d\n", *f.config, cfg.Version)

	rules, err := configv2.LoadRules(rulesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lotsmand -config-v2: rules: %v\n", err)
		return 1
	}
	fmt.Printf("rules %s: %d loaded, %d enabled\n", rulesDir, len(rules), countEnabled(rules))

	adapters := zapret.StrategyEngineAdapters()
	strategies := bundleStrategiesFor(*f.store, adapters, cfg, note)
	if err := configv2.ValidateReferencesAll(cfg, rules, strategies); err != nil {
		note("reference: %v", err)
	}

	providers := v2Providers(cfg, *f.rulesets, *f.plain, note)
	domainShapes, ipShapes := map[string]configv2.ListShape{}, map[string]configv2.ListShape{}
	domainDefs, ipDefs := cfg.Lists.Definitions()
	for name, def := range domainDefs {
		domainShapes[name] = loadV2List(def, providers, lists.HostlistCompiler{EngineName: "nfqws1"}, note)
	}
	for name, def := range ipDefs {
		ipShapes[name] = loadV2List(def, providers, lists.SingboxRuleSetCompiler{}, note)
	}

	gaps := configv2.Coherence(rules, domainShapes, ipShapes)
	fmt.Printf("coherence: %d gap(s)\n", len(gaps))
	for _, gap := range gaps {
		fmt.Printf("  %s: %s (%s)\n", gap.Service, gap.Kind, gap.Detail)
	}

	// Project onto the runtime Config and REPORT what the projection cannot carry.
	//
	// "The document is valid" and "the daemon will do the same thing with it" are
	// different claims, and only the second one matters at run time. A v2 file
	// loses the split-DNS block, the uTLS fingerprint, the target sing-box version
	// and the declared strategies; every one of those is a working system that
	// would then run at a default nobody chose, with the status page green. The
	// projection is the only place that knows, so it says so here rather than in
	// a comment nobody re-reads.
	projected, runtimeGaps, projErr := config.FromV2(cfg, rules)
	if projErr != nil {
		problems = append(problems, fmt.Sprintf("v2 → runtime projection failed: %v", projErr))
	} else {
		fmt.Printf("\nruntime projection: %d service(s), %d device(s), %d subscription(s), %d pool(s)\n",
			len(projected.Registry.Services), len(projected.Devices),
			len(projected.Subscriptions), len(projected.Pools.Pools))
		if len(projected.DisabledServices) > 0 {
			fmt.Printf("  disabled (not routable): %s\n", strings.Join(projected.DisabledServices, ", "))
		}
		fmt.Printf("\nNOT carried into the runtime by v2 — these run at defaults:\n")
		for _, gap := range runtimeGaps {
			fmt.Printf("  - %s\n", gap)
		}
	}

	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d problem(s):\n", len(problems))
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", problem)
		}
		return 1
	}
	fmt.Println("\nconfiguration is valid; nothing was applied")
	return 0
}

func countEnabled(rules []configv2.Rule) int {
	n := 0
	for _, r := range rules {
		if r.Enabled {
			n++
		}
	}
	return n
}

func bundleStrategiesFor(root string, adapters map[string]strategystore.EngineAdapter, cfg *configv2.Configuration, note func(string, ...any)) map[string][]string {
	out := map[string][]string{}
	bundles, err := strategystore.LoadDir(root)
	if err != nil {
		note("strategy store %s: %v", root, err)
		return out
	}
	byEngine := map[string][]string{}
	for _, bundle := range bundles {
		adapter, ok := adapters[bundle.Manifest.Engine]
		if !ok {
			note("bundle %s: unknown engine %q", bundle.Manifest.ID, bundle.Manifest.Engine)
			continue
		}
		if err := adapter.Validate(bundle); err != nil {
			note("bundle %s: %v", bundle.Manifest.ID, err)
			continue
		}
		byEngine[bundle.Manifest.Engine] = append(byEngine[bundle.Manifest.Engine], bundle.Manifest.ID)
	}
	for engine := range byEngine {
		sort.Strings(byEngine[engine])
	}
	for name, instance := range cfg.Desync.Instances {
		out[name] = byEngine[instance.Engine]
	}
	return out
}

func v2Providers(cfg *configv2.Configuration, rulesetDir, plainPath string, note func(string, ...any)) lists.Providers {
	decompiler := rulesets.SingboxDecompiler{}
	names := make([]string, 0, len(cfg.DataSources))
	for name := range cfg.DataSources {
		names = append(names, name)
	}
	sort.Strings(names)

	var sources []lists.SourceConfig
	for _, name := range names {
		entry := lists.SourceConfig{Name: name, Decompiler: decompiler}
		switch {
		case plainPath != "":
			entry.PlainPath = plainPath
		case rulesetDir != "":
			entry.LiveDir = rulesetDir
		default:
			note("data source %q: pass -rulesets-v2 or -plain-v2, or its lists cannot be resolved", name)
			continue
		}
		sources = append(sources, entry)
	}
	registry, err := lists.NewRegistry(sources)
	if err != nil {
		note("data sources: %v", err)
		return lists.Providers{}
	}
	return registry.Providers()
}

func loadV2List(def lists.Definition, providers lists.Providers, compiler lists.Compiler, note func(string, ...any)) configv2.ListShape {
	snapshot, err := def.LoadWith(context.Background(), providers, nil)
	if err != nil {
		note("list %q: %v", def.Name, err)
		return configv2.ListShape{Empty: true}
	}
	if _, err := compiler.Compile(snapshot); err != nil {
		note("list %q: %v", def.Name, err)
	}
	if snapshot.Invalid > 0 {
		fmt.Printf("  list %-16s %d entries (%d unparsable)\n", def.Name, len(snapshot.Values), snapshot.Invalid)
	} else {
		fmt.Printf("  list %-16s %d entries\n", def.Name, len(snapshot.Values))
	}
	if !snapshot.UpdatedAt.IsZero() && time.Since(snapshot.UpdatedAt) > 24*time.Hour {
		fmt.Printf("  list %-16s data is from %s\n", def.Name, snapshot.UpdatedAt.Format(time.RFC3339))
	}
	return configv2.ListShape{Empty: len(snapshot.Values) == 0}
}
