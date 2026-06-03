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
