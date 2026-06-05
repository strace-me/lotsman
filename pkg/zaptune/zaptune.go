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
	"strings"

	"github.com/strace-me/lotsman/pkg/nfqwsgen"
	"github.com/strace-me/lotsman/pkg/registry"
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

// BlockFor builds the single nfqws --new block for a service from the recipe the
// tester chose: the WHOLE service (all its inline domains) under one recipe. This
// is what keeps the service coherent — it is never split across blocks here. Pure.
func BlockFor(svc registry.Service, recipe strategycat.Recipe) nfqwsgen.Block {
	return nfqwsgen.Block{Service: svc.Name, Domains: svc.Domains, Recipe: recipe}
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
}

// Compose builds the nfqws config for the services CURRENTLY on a zapret rung
// (the caller filters by Brain state — per-RULE, not per-service). Each service
// contributes one --new block (all its domains, its picked recipe). Covered is
// true only when EVERY given service got a recipe and at least one block was
// produced — the caller applies the composed config ONLY then, otherwise it keeps
// the existing whole-config (e.g. Flowseal alt12). This is because a whole-script
// strategy and composed recipe blocks are different nfqws-config forms that do
// not mix per-service (see docs/DESIGN-strategy-generator.md). Pure — no I/O.
func Compose(services []registry.Service, recipes []strategycat.Recipe, pick Picker) Plan {
	plan := Plan{Chosen: map[string]string{}}
	var blocks []nfqwsgen.Block
	for _, svc := range services {
		// Candidates: target-class-narrowed AND domain-renderable (10b only fills
		// {{DOMAINS}} — recipes needing {{IPSET}}/{{GAME_PORTS}}/… are not yet usable).
		var cands []strategycat.Recipe
		for _, r := range CandidateRecipes(svc, recipes) {
			if usableForDomains(svc, r) {
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
		blocks = append(blocks, BlockFor(svc, r))
		plan.Chosen[svc.Name] = r.ID
	}
	plan.Covered = len(plan.Uncovered) == 0 && len(blocks) > 0
	if plan.Covered {
		plan.Args = nfqwsgen.Compose(blocks)
	}
	return plan
}

// usableForDomains reports whether a recipe can be rendered for a service using
// ONLY the service's domains: the service must have inline domains, and the
// recipe must carry no placeholder other than {{DOMAINS}}. Recipes needing
// {{IPSET}} (CIDR file), {{GAME_PORTS}}, etc. require IP/port templating not done
// in 10b, so they are not yet candidates (the service then stays on the existing
// whole-config until that lands). Pure.
func usableForDomains(svc registry.Service, r strategycat.Recipe) bool {
	if len(svc.Domains) == 0 {
		return false
	}
	for _, a := range r.NfqwsArgs {
		if strings.Contains(strings.ReplaceAll(a, "{{DOMAINS}}", ""), "{{") {
			return false // an unfillable placeholder remains
		}
	}
	return true
}

// FirstPicker is the 10b seed picker: take the first eligible candidate (catalog
// order = cold-start preference). 10c replaces this with a KB-ranked picker.
func FirstPicker(_ registry.Service, candidates []strategycat.Recipe) (strategycat.Recipe, bool) {
	if len(candidates) == 0 {
		return strategycat.Recipe{}, false
	}
	return candidates[0], true
}
