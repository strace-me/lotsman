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

// v2Check walks the whole v2 chain READ-ONLY and reports what would happen:
// config + rules decode, every reference resolves, every named list loads from
// its data source and compiles into the representation each engine needs, every
// strategy bundle passes its own engine's validator, and the coherence gaps are
// printed. It applies nothing and needs no root, so it can run anywhere.
func v2Check(args []string) {
	fs := flag.NewFlagSet("v2-check", flag.ExitOnError)
	configPath := fs.String("config", configv2.DefaultPath, "path to configuration.yaml")
	rulesDir := fs.String("rules", configv2.RulesDir, "dir holding <id>.yaml rule files")
	storeRoot := fs.String("store", "/etc/lotsman/desync/strategies", "strategy bundle root")
	rulesetDir := fs.String("rulesets", "", "compiled .srs root (enables provider-backed lists)")
	plainPath := fs.String("plain", "", "plain rule-set file (enables provider-backed lists)")
	from := fs.String("from", "", "v1 config to report a migration for, without applying it")
	desyncStrategy := fs.String("desync-strategy", "", "strategy bundle name for v1 `class: zapret` rungs that name none")
	_ = fs.Parse(args)

	var problems []string
	note := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	cfg, err := configv2.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "v2-check: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("configuration %s: version %d, %d data source(s), %d domain list(s), %d ip list(s), %d pool(s)\n",
		*configPath, cfg.Version, len(cfg.DataSources), len(cfg.Lists.Domains), len(cfg.Lists.IPs), len(cfg.Pools))
	for name, instance := range sortedInstances(cfg) {
		fmt.Printf("  desync instance %-16s engine=%-7s table=%s\n", name, instance.Engine, instance.Table)
	}

	rules, err := configv2.LoadRules(*rulesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "v2-check: rules: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rules %s: %d loaded\n", *rulesDir, len(rules))
	for _, rule := range rules {
		state := "on"
		if !rule.Enabled {
			state = "OFF"
		}
		fmt.Printf("  %-14s prio=%-4d %-3s chain=%s\n", rule.ID, rule.Priority, state, chainSummary(rule))
	}

	adapters := map[string]strategystore.EngineAdapter{
		zapret.NfqwsEngine{}.Engine():  zapret.NfqwsEngine{},
		zapret.Nfqws2Engine{}.Engine(): zapret.Nfqws2Engine{},
	}
	// The store is keyed by ENGINE (strategies/<engine>/<id>), while an instance
	// has its own name (desync.instances.nfqws1-main). A bundle therefore belongs
	// to every instance running that engine — matching it by directory name
	// instead would fail to resolve any real install.
	strategies := bundleStrategies(*storeRoot, adapters, cfg, note)
	for _, name := range sortedStrategyKeys(strategies) {
		fmt.Printf("  strategy %-16s %s\n", name, strings.Join(strategies[name], ", "))
	}

	// Every broken reference in one pass. A preflight that stops at the first
	// sends the operator round the loop once per mistake.
	for _, rule := range rules {
		if err := rule.ValidateReferences(cfg, strategies); err != nil {
			note("reference: %v", err)
		}
	}

	// List resolution is the step that turns a config into real traffic, so it
	// is the one that must be exercised before anything else is trusted.
	providers := buildProviders(cfg, *rulesetDir, *plainPath, note)
	domainShapes, ipShapes := map[string]configv2.ListShape{}, map[string]configv2.ListShape{}
	// immutable: sources under the nix store are content-addressed and replaced
	// by a store path, never edited in place.
	immutable := strings.HasPrefix(filepath.Clean(*plainPath), "/nix/store/") || *plainPath == ""
	domainDefs, ipDefs := cfg.Lists.Definitions()
	for name, def := range domainDefs {
		domainShapes[name] = loadOne(def, providers, lists.HostlistCompiler{EngineName: "nfqws1"}, immutable, note)
	}
	for name, def := range ipDefs {
		ipShapes[name] = loadOne(def, providers, lists.SingboxRuleSetCompiler{}, immutable, note)
	}

	if gaps := configv2.Coherence(rules, domainShapes, ipShapes); len(gaps) > 0 {
		fmt.Printf("coherence gaps:\n  %s\n", configv2.DescribeGaps(gaps))
	} else {
		fmt.Println("coherence: no gaps")
	}

	if *from != "" {
		reportMigration(*from, *desyncStrategy, note)
	}

	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "\nv2-check found %d problem(s):\n", len(problems))
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", problem)
		}
		os.Exit(1)
	}
	fmt.Println("\nv2-check: ok")
}

func chainSummary(rule configv2.Rule) string {
	parts := make([]string, 0, len(rule.Chain))
	for _, step := range rule.Chain {
		switch {
		case step.Action.Desync != nil:
			parts = append(parts, step.Tier+":desync:"+step.Action.Desync.Strategy)
		case step.Action.VPN != nil:
			parts = append(parts, step.Tier+":vpn:"+step.Action.VPN.Pool)
		case step.Action.Direct:
			parts = append(parts, step.Tier+":direct")
		case step.Action.Bypass:
			parts = append(parts, step.Tier+":bypass")
		}
	}
	return strings.Join(parts, " -> ")
}

func sortedInstances(cfg *configv2.Configuration) map[string]configv2.Instance {
	// Go map order is random; a report that reshuffles between runs is a report
	// nobody can diff.
	out := map[string]configv2.Instance{}
	names := make([]string, 0, len(cfg.Desync.Instances))
	for name := range cfg.Desync.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out[name] = cfg.Desync.Instances[name]
	}
	return out
}

func sortedStrategyKeys(strategies map[string][]string) []string {
	keys := make([]string, 0, len(strategies))
	for key := range strategies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func bundleStrategies(root string, adapters map[string]strategystore.EngineAdapter, cfg *configv2.Configuration, note func(string, ...any)) map[string][]string {
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

func buildProviders(cfg *configv2.Configuration, rulesetDir, plainPath string, note func(string, ...any)) lists.Providers {
	decompiler := rulesets.SingboxDecompiler{}
	sources := make([]lists.SourceConfig, 0, len(cfg.DataSources))
	for _, name := range sortedDataSources(cfg) {
		source := cfg.DataSources[name]
		if source.Repo == "" && source.Type == "" {
			continue
		}
		entry := lists.SourceConfig{Name: name, Decompiler: decompiler}
		if plainPath != "" {
			entry.PlainPath = plainPath
		} else if rulesetDir != "" {
			entry.LiveDir = rulesetDir
		} else {
			note("data source %q: no -rulesets or -plain given, its lists cannot be resolved", name)
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

func sortedDataSources(cfg *configv2.Configuration) []string {
	names := make([]string, 0, len(cfg.DataSources))
	for name := range cfg.DataSources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func loadOne(def lists.Definition, providers lists.Providers, compiler lists.Compiler, immutable bool, note func(string, ...any)) configv2.ListShape {
	snapshot, err := def.LoadWith(context.Background(), providers, nil)
	if err != nil {
		note("list %q: %v", def.Name, err)
		return configv2.ListShape{Empty: true}
	}
	if _, err := compiler.Compile(snapshot); err != nil {
		note("list %q: %v", def.Name, err)
	}
	// Unparsable entries make the list narrower than its source, which matters,
	// but a handful of malformed lines in a community list is not a reason to
	// refuse a deploy — it is reported, not gated on.
	freshness := "fresh"
	if snapshot.Invalid > 0 {
		freshness = fmt.Sprintf("%d unparsable", snapshot.Invalid)
	}
	if snapshot.Stale(6 * time.Hour) {
		if immutable {
			// A nix-store source cannot change under us, so its mtime is the
			// store's build time and "older than 6h" is a statement about the
			// build, not about staleness. Reporting it would cry wolf forever.
			freshness += ", immutable source"
		} else {
			note("list %q: data is older than 6h (last update %s)", def.Name, snapshot.UpdatedAt.Format(time.RFC3339))
		}
	}
	fmt.Printf("  list %-16s %-8s %d entries from %d source(s) [%s]\n", def.Name, def.Kind, len(snapshot.Values), snapshot.Sources, freshness)
	return configv2.ListShape{Empty: len(snapshot.Values) == 0}
}

func reportMigration(path, desyncStrategy string, note func(string, ...any)) {
	data, err := os.ReadFile(path)
	if err != nil {
		note("migration: %v", err)
		return
	}
	document, err := config.ParseDocument(data)
	if err != nil {
		note("migration: %v", err)
		return
	}
	migration, err := config.MigrateToV2(document, func(string) (string, error) {
		return desyncStrategy, nil
	})
	if err != nil {
		note("migration %s: %v", path, err)
		return
	}
	fmt.Printf("migration of %s: %d rule(s), %d warning(s)\n", path, len(migration.Rules), len(migration.Warnings))
	for _, rule := range migration.Rules {
		fmt.Printf("  %-14s prio=%-4d %s\n", rule.ID, rule.Priority, chainSummary(rule))
	}
	for _, warning := range migration.Warnings {
		fmt.Printf("  ! %s\n", warning)
	}
}
