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
	"github.com/strace-me/lotsman/pkg/zapret"
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
	// PinsIgnored names the services whose pinned recipe was not among the usable
	// candidates, so the caller can say so out loud. A pin that quietly does
	// nothing turns every experiment run under it into a measurement of something
	// else.
	PinsIgnored []PinIgnored
}

// PinIgnored is one pin the composer could not honour.
type PinIgnored struct {
	Service string
	Want    string
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
// Pins name the recipe the BRAIN asked for, per service — a `strategy_id` on the
// chain step the service currently occupies. Honouring it is the difference
// between a pin and a suggestion: without this the composer ranked candidates by
// the knowledge base and quietly ran something else, so an operator who pinned
// ALT12 was testing whatever the picker preferred that minute. nil = no pins.
type Pins map[string]string

// ComposePinned is Compose with the brain's per-service strategy pins honoured.
// A pin is used when it names a recipe that is actually among the renderable
// candidates for that service; when it does not, the pick falls back to the
// picker and the caller is told which pin could not be honoured, because a pin
// that silently does nothing is worse than no pin at all.
func ComposePinned(services []registry.Service, recipes []strategycat.Recipe, pick Picker, resolve Resolver, hostlistDir string, pins Pins) Plan {
	return compose(services, recipes, pick, resolve, hostlistDir, pins)
}

// Compose composes with no pins — the router's path, where the executor honours
// a chain strategy_id by switching scripts rather than by choosing a recipe.
func Compose(services []registry.Service, recipes []strategycat.Recipe, pick Picker, resolve Resolver, hostlistDir string) Plan {
	return compose(services, recipes, pick, resolve, hostlistDir, nil)
}

func compose(services []registry.Service, recipes []strategycat.Recipe, pick Picker, resolve Resolver, hostlistDir string, pins Pins) Plan {
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
		// A pin wins over the ranking when it names one of the renderable
		// candidates. When it does not, say which pin was ignored and why — an
		// operator who pinned a recipe and silently got another one is testing
		// something other than what they think.
		var r strategycat.Recipe
		var ok bool
		if want := pins[svc.Name]; want != "" {
			for _, c := range cands {
				if c.ID == want {
					r, ok = c, true
					break
				}
			}
			if !ok {
				plan.PinsIgnored = append(plan.PinsIgnored, PinIgnored{Service: svc.Name, Want: want})
			}
		}
		if !ok {
			r, ok = pick(svc, cands)
		}
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
	for _, a := range r.AllArgs() {
		if strings.Contains(a, "{{DOMAINS}}") {
			hasDomains = true
		}
		if strings.Contains(strings.ReplaceAll(a, "{{DOMAINS}}", ""), "{{") {
			return false // an unfillable placeholder remains
		}
	}
	return hasDomains || narrowlyPortScoped(r)
}

// maxDomainlessPorts bounds how much traffic a recipe with no host selector may
// claim. Discord's voice profile is 152 ports (19294-19344 plus 50000-50100) and
// is a targeted rule; a games-class STUN profile spanning 1024-65535 is most of
// the internet and would desync traffic nobody asked about.
const maxDomainlessPorts = 4096

// narrowlyPortScoped reports whether a recipe without {{DOMAINS}} is nevertheless
// properly scoped — by a small port range, and by the layer-7 protocol.
//
// Requiring a host selector was too strong, and it cost the feature it was
// protecting: raw UDP carries no SNI, so voice media CANNOT be selected by domain
// at all, and the one recipe that fixes Discord voice (flowseal-discord-stun,
// straight out of Flowseal's ALT12) was refused as "global" while being narrower
// than most domain-scoped rules. Ports plus --filter-l7 is a real scope; it is
// simply not a domain one.
//
// Both conditions are required. The port bound alone would admit a wide range with
// no protocol check, and --filter-l7 alone would admit it across every port.
func narrowlyPortScoped(r strategycat.Recipe) bool {
	hasL7 := false
	for _, a := range r.AllArgs() {
		if strings.HasPrefix(a, "--filter-l7=") {
			hasL7 = true
			break
		}
	}
	if !hasL7 {
		return false
	}
	scope := zapret.CaptureFromArgs(r.AllArgs())
	n := scope.PortCount()
	return n > 0 && n <= maxDomainlessPorts
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
			r := score(svc.Name, candidates[i].ID)
			switch {
			case r > bestRate:
				best, bestRate = i, r
			case r == bestRate && candidates[i].Consensus > candidates[best].Consensus:
				// Ties are the COLD case, not a rare one: an untouched KB scores
				// every candidate at the same prior, so without a tiebreak the
				// pick is catalog order and the engine restarts its way down a
				// list of 120. Consensus — how many independent bundles ship this
				// exact recipe — is the only prior available without measuring
				// anything, and it costs nothing to consult. It never outranks a
				// real outcome, because a differing score decides first.
				best = i
			}
		}
		return candidates[best], true
	}
}

// UsableCandidates returns the recipes that could ACTUALLY be composed for svc,
// applying exactly the filter compose() applies before it honours a pin.
//
// It exists because a caller offering a candidate outside this set gets the
// picker's choice back instead, silently — the composer reports the ignored pin,
// but a caller that does not consult this list simply proposes recipes that can
// never be honoured. Measured on hardware the day the sandbox first ran: the
// rotation offered zms-dv3/5/6/7/14 and flowseal-alt12-discord to rules they were
// never renderable for, every single test came back "pin not honoured", and not
// one candidate ever reached the test lane. The gate looked like it was working —
// it refused everything — while nothing was being measured at all.
func UsableCandidates(svc registry.Service, recipes []strategycat.Recipe) []strategycat.Recipe {
	port := ProbePort(svc)
	var out []strategycat.Recipe
	for _, r := range CandidateRecipes(svc, recipes) {
		if recipeRenderable(r) && recipeCoversPort(r, port) {
			out = append(out, r)
		}
	}
	return out
}
