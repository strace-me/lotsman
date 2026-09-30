package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/lists"
	"github.com/strace-me/lotsman/pkg/rulesets"
	"github.com/strace-me/lotsman/pkg/singbox"
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
	// run makes the daemon START on a v2 document instead of reporting on one. It is
	// a separate flag from -config-v2 on purpose: that one means "validate and EXIT",
	// and silently changing the meaning of a flag that already exists is how a
	// documented safety property disappears.
	run *string
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
		run: fs.String("run-v2", "",
			"START the daemon on a v2 configuration.yaml (or its directory). Mutually exclusive with -config; -config-v2 only validates and exits"),
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

	// Accept the DIRECTORY as well as the file, because a v2 deployment is
	// configuration.yaml + rules/ as a unit. This is the same resolution -config
	// does, so the preflight can be pointed at what the daemon is pointed at.
	configPath, rulesDir := *f.config, *f.rules
	if info, serr := os.Stat(configPath); serr == nil && info.IsDir() {
		if rulesDir == "" {
			rulesDir = filepath.Join(configPath, "rules")
		}
		configPath = filepath.Join(configPath, "configuration.yaml")
	}
	cfg, err := configv2.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lotsmand -config-v2: %v\n", err)
		return 1
	}
	if rulesDir == "" {
		rulesDir = filepath.Join(filepath.Dir(configPath), "rules")
	}
	fmt.Printf("configuration %s: version %d\n", configPath, cfg.Version)

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

	// One scratch root for the whole run, removed on the way out including on an early
	// return. Per-source temp dirs leaked: each preflight left a partial copy of a
	// 27 MB bundle in /tmp, and a check is the last thing that should accumulate state.
	scratchRoot, err = os.MkdirTemp("", "lotsman-v2src-")
	if err != nil {
		note("scratch space: %v", err)
	} else {
		defer os.RemoveAll(scratchRoot)
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

// v2Providers builds list providers for the preflight.
//
// An explicit -rulesets-v2/-plain-v2 wins, because the operator told us where the
// compiled files are and guessing would be rude. Failing that, a source that names a
// repo is FETCHED into a scratch directory and read from there.
//
// That fetch is the gap this closes. The preflight is the answer to "is this config
// sane?", and a list it cannot resolve is a list it cannot judge — so before, the
// only way to check a config whose lists lived in data_sources was to run the daemon
// first and let it download them. Checking a config by running the thing it checks is
// backwards: the download could fail, and so could the config, and you could not tell
// which. Now the preflight resolves them itself, into a temp dir it deletes, so it
// still touches nothing of yours.
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
			src := cfg.DataSources[name]
			// Bounded, because this is a CHECK and a check that can hang is not a
			// check. Without it a slow or unreachable host turns "is my config sane?"
			// into a command with no timeout, which is the one property an operator
			// running it over SSH on a router actually needs.
			ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
			dir, err := materializeSource(ctx, name, src, requiredTags(cfg), note)
			cancel()
			if err != nil {
				note("data source %q: %v", name, err)
				continue
			}
			entry.LiveDir = dir
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

// entryCounter counts rules by parsing the compiled .srs, so a preflight can run the
// shrink guard on a machine that has no sing-box binary at all — which is exactly the
// machine you want to check a config from. The daemon and client count by asking
// sing-box, because there it is already running and its answer is the one that counts;
// here, parsing the file is both available and the same number.
type entryCounter struct{}

func (entryCounter) Count(srsPath string) (int, error) {
	domains, cidrs, err := rulesets.SingboxDecompiler{}.Entries(srsPath)
	if err != nil {
		return 0, err
	}
	return len(domains) + len(cidrs), nil
}

// fetchTimeout bounds the scratch fetch. Generous enough for a 27 MB bundle on a slow
// link, short enough that a wedged download is a failed check rather than a hung shell.
const fetchTimeout = 2 * time.Minute

// materializeSource fetches one data source's bundle into a scratch dir. It is the
// shared updater, not a second fetcher: the release selection, the extraction and the
// validity check are the same code that will later put these files in service, so the
// preflight cannot pass on a release the daemon would reject.
// scratchRoot is the run-wide temp parent for materialized sources. Empty means the
// MkdirTemp failed and the fetch is skipped rather than scattered across /tmp.
var scratchRoot string

func materializeSource(ctx context.Context, name string, src configv2.DataSource, required []string, note func(string, ...any)) (string, error) {
	// A source with no repo is the plain form: it points at files somebody put on
	// disk, and this is not the place to go looking for them.
	if src.Repo == "" {
		return "", fmt.Errorf("does not say where its lists come from; pass -rulesets-v2 or -plain-v2")
	}
	if scratchRoot == "" {
		return "", fmt.Errorf("no scratch space available")
	}
	dir, err := os.MkdirTemp(scratchRoot, name+"-")
	if err != nil {
		return "", err
	}
	up := &rulesets.Updater{
		Repo: src.Repo, Required: required, LiveDir: dir, Pin: src.Pin,
		AutoBump: true, PathFor: singbox.RuleSetRelPath,
		Releaser: rulesets.GitHubReleaser{},
		Counter:  entryCounter{},
		Log:      slog.New(slog.DiscardHandler),
	}
	if err := up.Update(ctx); err != nil {
		return "", err
	}
	note("data source %q: fetched %s%s into scratch space to check its lists", name, src.Repo,
		pinSuffix(src.Pin))
	return dir, nil
}

func pinSuffix(pin string) string {
	if pin == "" {
		return " (latest)"
	}
	return " at " + pin
}

// requiredTags is the union of rule-set tags the document references. The updater
// takes only what the config needs, so a preflight of a config using three lists does
// not pull the whole bundle — which is the difference between a check and a wait.
//
// The tags come from configv2.ResolveListTag, the runtime's own resolution. An
// earlier version reimplemented the naming here and got it wrong — it read the map key
// as the list name and produced "geosite-geosite" — so the updater was handed tags no
// bundle contains, installed nothing, and the preflight then reported all nine lists
// as missing files. A second copy of a naming rule is a second answer, and here the
// second answer was confidently wrong.
func requiredTags(cfg *configv2.Configuration) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name, kind string) {
		tag, err := configv2.ResolveListTag(cfg, name, kind)
		if err != nil || seen[tag] {
			return
		}
		seen[tag] = true
		out = append(out, tag)
	}
	for name := range cfg.Lists.Domains {
		add(name, "geosite")
	}
	for name := range cfg.Lists.IPs {
		add(name, "geoip")
	}
	return out
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
