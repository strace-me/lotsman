package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// The bridge is the claim that makes a v2 cutover a parser swap rather than a
// rewrite: the runtime consumes registry.Service, so a v2 document must produce the
// registry the v1 parser produces from the same input. This compares them on the
// REAL production config, service by service, because a fixture would not contain
// the shapes that actually differ — two VPN rungs in one tier, an unpinned desync
// rung, a catch-all next to a negative-priority sibling.
func TestV2BridgeProducesTheRegistryV1Does(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "r5s-production.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("production example not available")
	}

	v1, err := Parse(data)
	if err != nil {
		t.Fatalf("v1 parse: %v", err)
	}
	if v1.Registry == nil || len(v1.Registry.Services) == 0 {
		t.Fatal("v1 produced no services")
	}

	doc, err := ParseDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	v2reg, err := configv2.BuildRegistry(migration.Configuration, migration.Rules, nil, nil)
	if err != nil {
		t.Fatalf("v2 bridge: %v", err)
	}

	if len(v2reg.Services) != len(v1.Registry.Services) {
		var v1names []string
		for name := range v1.Registry.Services {
			v1names = append(v1names, name)
		}
		t.Fatalf("service count v1=%d v2=%d (v1: %v)", len(v1.Registry.Services), len(v2reg.Services), v1names)
	}

	emergencyCarried := false
	for name, want := range v1.Registry.Services {
		got, ok := v2reg.Services[name]
		if !ok {
			t.Errorf("service %q is missing from the v2 registry", name)
			continue
		}
		if want.Priority != got.Priority {
			t.Errorf("service %q: priority %d -> %d", name, want.Priority, got.Priority)
		}
		if len(want.Chain) != len(got.Chain) {
			t.Errorf("service %q: chain %d -> %d\n v1: %v\n v2: %v", name, len(want.Chain), len(got.Chain), chainOf(want), chainOf(got))
			continue
		}
		for i := range want.Chain {
			// Two differences are INTENDED and are checked as such rather than
			// waved through:
			//
			//  - the state, because v2 replaced the ALT_ZAPRET/LOCKED labels with a
			//    tier, and the state is only ever a label;
			//  - the emergency class. v1 had an `emergency` class with NO executor, so
			//    a rule that reached it failed with "no executor for class" — that is
			//    the incident CHANGELOG records. v2 carries the pool on the vpn
			//    action, which makes the rung reachable. That is a fix, and a change
			//    of behaviour, so it is asserted rather than assumed.
			if want.Chain[i].StrategyClass != got.Chain[i].StrategyClass {
				if want.Chain[i].StrategyClass == strategy.ClassEmergency && got.Chain[i].StrategyClass == strategy.ClassVPN {
					emergencyCarried = true
					continue
				}
				t.Errorf("service %q step %d: class %q -> %q", name, i, want.Chain[i].StrategyClass, got.Chain[i].StrategyClass)
			}
			if !sameStrategy(want.Chain[i], got.Chain[i]) {
				t.Errorf("service %q step %d: strategy %q -> %q", name, i, want.Chain[i].StrategyID, got.Chain[i].StrategyID)
			}
		}
	}
	if !emergencyCarried {
		t.Error("the production config has emergency rungs but none reached the bridge; the fix above is untested")
	}
}

// The production config does use the emergency class, so the fix above must
// actually be exercised — otherwise the assertion could pass by never running.
func TestBridgeAuditExercisesTheEmergencyRung(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "r5s-production.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("production example not available")
	}
	doc, err := ParseDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEmergencyRung(doc) {
		t.Skip("the production config no longer has an emergency rung")
	}
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !warnsAboutEmergency(migration.Warnings) {
		t.Errorf("an emergency rung changes behaviour from broken to working and must be reported: %v", migration.Warnings)
	}
}

func hasEmergencyRung(doc *Document) bool {
	for _, s := range doc.Services {
		for _, step := range s.Chain {
			if step.Class == strategy.ClassEmergency {
				return true
			}
		}
	}
	return false
}

func warnsAboutEmergency(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, "emergency") {
			return true
		}
	}
	return false
}

// sameStrategy tolerates exactly one intended difference: a v1 desync rung with no
// strategy_id means "the KB picks", which v2 spells `auto`.
func sameStrategy(want, got registry.ChainStep) bool {
	if want.StrategyID == "" && want.StrategyClass == strategy.ClassZapret {
		return got.StrategyID == configv2.AutoStrategy
	}
	return want.StrategyID == got.StrategyID
}

func chainOf(s registry.Service) []string {
	out := make([]string, 0, len(s.Chain))
	for _, step := range s.Chain {
		out = append(out, step.StrategyClass+":"+step.StrategyID)
	}
	return out
}
