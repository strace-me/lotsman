// Package zaptune is the per-SERVICE zapret recipe tuning glue — the half that
// turns "this service has blocked domains" into "this service's zapret rung uses
// recipe X". It deliberately keeps the SERVICE as the unit so services do not
// fall apart: one recipe per service, one --new block carrying ALL of that
// service's domains, one rung. Per-domain probing (pkg/domainscan) is only the
// OBSERVABILITY that flags which services to tune and on which domains to judge
// them — never a license to split a service's domains across different recipes
// or paths.
//
// Roles stay as everywhere else: Brain owns the rung (zapret/VPN/…), this advises
// the recipe FOR the zapret rung (the caller runs tester.Resolve over the
// candidate recipes, probing the service's blocked domains), and the applier is
// the single writer. If a service genuinely needs two different recipes for
// different domains, that means it is mis-grouped — the fix is to split it into
// two services in config, not to auto-fragment it here.
package zaptune

import (
	"path/filepath"
	"strings"

	"github.com/strace-me/lotsman/pkg/nfqwsgen"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// classesFor maps a service to the recipe target-classes worth trying for it,
// by its profile/identity. Pure.
func classesFor(svc registry.Service) []strategycat.TargetClass {
	switch {
	case svc.Name == "discord" || svc.Profile == "voice":
		return []strategycat.TargetClass{strategycat.ClassDiscordTCP, strategycat.ClassGeneralTLS}
	case svc.Profile == "streaming":
		return []strategycat.TargetClass{strategycat.ClassQUIC, strategycat.ClassGeneralTLS}
	case svc.Profile == "gaming":
		return []strategycat.TargetClass{strategycat.ClassGeneralTLS, strategycat.ClassGames}
	default:
		return []strategycat.TargetClass{strategycat.ClassGeneralTLS}
	}
}

// CandidateRecipes returns the catalog recipes worth A/B-testing for a service,
// narrowed to its target-classes (so a gaming launcher tries TCP-TLS recipes, a
// streaming service tries QUIC, etc.). Pure — the live caller feeds these to
// tester.Resolve. Order follows the catalog.
func CandidateRecipes(svc registry.Service, catalog []strategycat.Recipe) []strategycat.Recipe {
	want := map[strategycat.TargetClass]bool{}
	for _, c := range classesFor(svc) {
		want[c] = true
	}
	var out []strategycat.Recipe
	for _, r := range catalog {
		if want[r.TargetClass] {
			out = append(out, r)
		}
	}
	return out
}

// BlockFor builds the single nfqws --new block for a service: the WHOLE service
// (all its domains — inline + resolved rule_sets) under one recipe. domains is
// the full set computed by serviceDomains. Keeping it one block keeps the service
// coherent (never split across blocks). Pure.
func BlockFor(svc registry.Service, recipe strategycat.Recipe, domains, exclude []string) nfqwsgen.Block {
	return nfqwsgen.Block{Service: svc.Name, Domains: domains, Recipe: recipe, Exclude: exclude}
}

// Resolver resolves a rule_set tag (e.g. "geosite-discord") to its plaintext
// domains, so nfqws desyncs the SAME domains sing-box ROUTES (coherence — LOT-32
// TM-1). ok=false means the tag could not be resolved. nil resolver = no rule_set
// resolution at all. Wire the live impl to `sing-box rule-set decompile`.
type Resolver func(ruleSetTag string) ([]string, bool)

// serviceDomains returns the FULL plaintext domain set nfqws must cover for a
// service: its inline domains PLUS the domains of every rule_set it routes
// (resolved via resolve). coherent is false when a rule_set cannot be resolved —
// composing then would silently miss those domains (the discord.com regression,
// where discord's gateway lives in geosite-discord), so such a service must NOT
// be composed; the caller keeps the existing whole-config instead. Pure.
func serviceDomains(svc registry.Service, resolve Resolver) (domains []string, coherent bool) {
	out := append([]string(nil), svc.Domains...)
	for _, rs := range svc.RuleSets {
		if resolve == nil {
			return nil, false
		}
		d, ok := resolve(rs)
		if !ok {
			return nil, false
		}
		out = append(out, d...)
	}
	return dedup(out), true
}

func dedup(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Picker chooses ONE recipe for a service from its eligible candidates (already
// narrowed to the service's target-classes and to domain-renderable recipes, in
// catalog order). ok=false skips the service (no suitable recipe). 10b injects a
// seed picker (first candidate); 10c/10e inject a KB-ranked / A-B picker. Pure.
type Picker func(svc registry.Service, candidates []strategycat.Recipe) (strategycat.Recipe, bool)

// Plan is the composed nfqws config for a set of zapret-active services (LOT-10b).
type Plan struct {
	Args      []string          // composed nfqws args (--new-separated); set only when Covered
	Covered   bool              // every given service got a recipe AND at least one block exists
	Uncovered []string          // services with no usable recipe (why Covered may be false)
	Chosen    map[string]string // service -> chosen recipe ID (for logging / KB)
	// Hostlists is path -> domains for the blocks scoped by FILE rather than by
	// inlined args (set only when Compose was given a hostlist dir). The caller
	// writes these; keeping the write out here leaves Compose pure. Writing them
	// only when the content actually changes is what makes membership changes
	// restart-free — nfqws re-reads a hostlist when its mtime moves.
	Hostlists map[string][]string
}

// Compose builds the nfqws config for the services CURRENTLY on a zapret rung
// (the caller filters by Brain state — per-RULE, not per-service). Each service
// contributes one --new block (all its domains, its picked recipe). Covered is
// true only when EVERY given service got a recipe and at least one block was
// produced — the caller applies the composed config ONLY then, otherwise it keeps
// the existing whole-config (e.g. Flowseal alt12). This is because a whole-script
// strategy and composed recipe blocks are different nfqws-config forms that do
// not mix per-service (see docs/DESIGN-strategy-generator.md). Pure — no I/O.
// hostlistDir, when non-empty, scopes each block by a per-service hostlist FILE
// under that directory instead of inlining its domains into the arguments. The
// files themselves are returned in Plan.Hostlists for the caller to write.
func Compose(services []registry.Service, recipes []strategycat.Recipe, pick Picker, resolve Resolver, hostlistDir string) Plan {
	plan := Plan{Chosen: map[string]string{}, Hostlists: map[string][]string{}}
	var blocks []nfqwsgen.Block
	for _, svc := range services {
		// Full domain set nfqws must cover (inline + resolved rule_sets). If a
		// rule_set can't be resolved, the service is NOT coherently composable —
		// skip it (caller keeps the existing config) rather than desync an
		// incomplete set and regress the missing domains (TM-1).
		domains, coherent := serviceDomains(svc, resolve)
		if !coherent {
			plan.Uncovered = append(plan.Uncovered, svc.Name)
			continue
		}
		// No domains at all is NOT the same as "could not be covered". A service can
		// legitimately have none — probe-only, or matched purely by IP like Discord
		// voice, or carrying a domain pack that has not been fetched yet. Excluding it
		// from the composition loses nothing, whereas counting it as uncovered makes
		// Covered false and stops the caller applying the desync for EVERY service:
		// one IP-only service would silently disarm the whole fleet.
		if len(domains) == 0 {
			continue
		}
		// Candidates: target-class-narrowed AND domain-renderable (fills {{DOMAINS}};
		// recipes needing {{IPSET}}/{{GAME_PORTS}}/… or with no {{DOMAINS}} are not usable).
		// Only recipes the canary can actually falsify: one scoped to ports the
		// service's probe never reaches would be applied blind and then blamed for a
		// failure it could not have caused.
		port := ProbePort(svc)
		var cands []strategycat.Recipe
		for _, r := range CandidateRecipes(svc, recipes) {
			if recipeRenderable(r) && recipeCoversPort(r, port) {
				cands = append(cands, r)
			}
		}
		if len(cands) == 0 {
			plan.Uncovered = append(plan.Uncovered, svc.Name)
			continue
		}
		r, ok := pick(svc, cands)
		if !ok {
			plan.Uncovered = append(plan.Uncovered, svc.Name)
			continue
		}
		b := BlockFor(svc, r, domains, dedup(svc.ExcludeDomains))
		if hostlistDir != "" {
			b.HostlistPath = filepath.Join(hostlistDir, svc.Name+".txt")
			plan.Hostlists[b.HostlistPath] = domains
		}
		blocks = append(blocks, b)
		plan.Chosen[svc.Name] = r.ID
	}
	plan.Covered = len(plan.Uncovered) == 0 && len(blocks) > 0
	if plan.Covered {
		plan.Args = nfqwsgen.Compose(blocks)
	}
	return plan
}

// recipeRenderable reports whether a recipe is a valid per-service DOMAIN-SCOPED
// block: it contains the {{DOMAINS}} placeholder (so it is pinned to the service's
// domains via --hostlist-domains, not a global block) and carries no OTHER
// placeholder ({{IPSET}}/{{GAME_PORTS}}/…) we cannot fill. A recipe without
// {{DOMAINS}} (e.g. a games-class global UDP STUN profile, --filter-udp=1024-65535
// with no hostlist) would desync unrelated traffic and is rejected. Pure.
func recipeRenderable(r strategycat.Recipe) bool {
	hasDomains := false
	for _, a := range r.NfqwsArgs {
		if strings.Contains(a, "{{DOMAINS}}") {
			hasDomains = true
		}
		if strings.Contains(strings.ReplaceAll(a, "{{DOMAINS}}", ""), "{{") {
			return false // an unfillable placeholder remains
		}
	}
	return hasDomains
}

// FirstPicker is the 10b seed picker: take the first eligible candidate (catalog
// order = cold-start preference). 10c replaces this with a KB-ranked picker.
func FirstPicker(_ registry.Service, candidates []strategycat.Recipe) (strategycat.Recipe, bool) {
	if len(candidates) == 0 {
		return strategycat.Recipe{}, false
	}
	return candidates[0], true
}

// discoveredClassFilter is the default nfqws filter scope per target-class for a
// DISCOVERED strategy (which carries only the desync technique — blockcheck does
// not scope it). The standard port set the curated catalog uses per class. games
// is omitted: it needs per-game ports, with no safe blanket default.
var discoveredClassFilter = map[strategycat.TargetClass][]string{
	strategycat.ClassGeneralTLS: {"--filter-tcp=80,443"},
	strategycat.ClassYouTube:    {"--filter-tcp=80,443"},
	strategycat.ClassDiscordTCP: {"--filter-tcp=443"},
	strategycat.ClassQUIC:       {"--filter-udp=443"},
}

// RecipesFromDefinitions converts operator-classified DISCOVERED strategies
// (LOT-10a `lotsmanctl harvest -target-class`) into composable recipes (LOT-10b):
// it scopes each technique-only strategy with its target-class's default filter
// and the {{DOMAINS}} placeholder, so a harvested strategy can join the curated
// strategycat pool the composer picks from. A definition with no TargetClass (un-
// asserted), no NFQWSArgs (script-backed), or an unknown/unscopable class (games)
// is skipped — it stays in the KB ranking but is not composed. Pure.
func RecipesFromDefinitions(defs []strategy.Definition) []strategycat.Recipe {
	var out []strategycat.Recipe
	for _, d := range defs {
		if d.TargetClass == "" || len(d.NFQWSArgs) == 0 {
			continue
		}
		class := strategycat.TargetClass(d.TargetClass)
		filter, ok := discoveredClassFilter[class]
		if !ok {
			continue // unknown / games: no safe default scope
		}
		args := make([]string, 0, len(filter)+1+len(d.NFQWSArgs))
		args = append(args, filter...)
		args = append(args, "--hostlist-domains={{DOMAINS}}")
		args = append(args, d.NFQWSArgs...)
		out = append(out, strategycat.Recipe{ID: d.ID, Provenance: "discovered", TargetClass: class, NfqwsArgs: args})
	}
	return out
}

// PromoteToRecipe turns a v7-tuner WINNER (a strategy already rendered to its
// engine's nfqws desync args, e.g. ["--dpi-desync=multisplit","--dpi-desync-split-pos=2"])
// into a composable Recipe for svc — the generated-side analogue of
// RecipesFromDefinitions. It scopes the technique with the service's primary
// target-class default filter and {{DOMAINS}}, so the new winner can immediately
// compete in composition and be persisted to the catalog. id is the stable
// strategy id (desynctune.StrategyID(args)). ok=false when the service's class has
// no safe blanket filter (e.g. games) — promote nothing rather than an unscoped block.
func PromoteToRecipe(svc registry.Service, id string, renderedArgs []string) (strategycat.Recipe, bool) {
	if id == "" || len(renderedArgs) == 0 {
		return strategycat.Recipe{}, false
	}
	classes := classesFor(svc)
	if len(classes) == 0 {
		return strategycat.Recipe{}, false
	}
	class := classes[0] // the service's primary class
	filter, ok := discoveredClassFilter[class]
	if !ok {
		return strategycat.Recipe{}, false // unscopable (games): no blanket default
	}
	args := make([]string, 0, len(filter)+1+len(renderedArgs))
	args = append(args, filter...)
	args = append(args, "--hostlist-domains={{DOMAINS}}")
	args = append(args, renderedArgs...)
	return strategycat.Recipe{ID: id, Provenance: "generated", TargetClass: class, NfqwsArgs: args}, true
}

// KBPicker (LOT-10c) ranks candidates by a learned success score (higher first),
// keeping catalog order as the tiebreak — so a COLD KB (all scores equal to the
// prior) picks the first candidate exactly like FirstPicker, and as recipes prove
// or fail in production the picker exploits that. score(service, recipeID) is the
// learned success rate in [0,1] (wire to kb.KB.Stats(...).Success); taking a plain
// func keeps zaptune free of a kb dependency. NOTE: the score only becomes
// meaningful once recipes are actually APPLIED and their probe outcomes recorded
// (kb.RecordOutcome) — i.e. once the composer is armed; in propose-only the KB has
// no recipe data and this behaves as the seed picker. Pure given score.
func KBPicker(score func(service, recipeID string) float64) Picker {
	return func(svc registry.Service, candidates []strategycat.Recipe) (strategycat.Recipe, bool) {
		if len(candidates) == 0 {
			return strategycat.Recipe{}, false
		}
		best, bestRate := 0, score(svc.Name, candidates[0].ID)
		for i := 1; i < len(candidates); i++ {
			if r := score(svc.Name, candidates[i].ID); r > bestRate {
				best, bestRate = i, r
			}
		}
		return candidates[best], true
	}
}
