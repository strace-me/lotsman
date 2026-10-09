package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// This is the migration audit the design calls for: v1 may only be retired AFTER
// a separate audit, and the audit has to be run against the REAL production config
// rather than a tidy fixture — the working file is what everyone actually depends
// on, and it is full of the shapes a fixture never has (two VPN rungs in one tier,
// an unpinned desync rung, a catch-all with a negative-priority sibling).
//
// The claim under test: every enabled v1 service survives as a v2 rule whose chain
// is the SAME ORDERED SEQUENCE of actions. Order is the whole semantics of a
// fallback ladder, so a rule that kept its rungs but reordered them would be a
// silent change of behaviour, and a rule that quietly lost one is worse.
func TestMigrationAuditOnTheRealProductionConfig(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "r5s-production.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("production example not available: %v", err)
	}
	doc, err := ParseLegacyDocument(data)
	if err != nil {
		t.Fatalf("the production config does not parse: %v", err)
	}

	// The v1 tree carried one strategy per instance, so a `class: zapret` rung with
	// no strategy_id becomes `auto`. Anything else would be a guess about which
	// bundle this box runs, and a wrong one is a desync that silently does nothing.
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatalf("migration failed on the real config: %v", err)
	}

	rules := map[string]configv2.Rule{}
	for _, r := range migration.Rules {
		rules[r.ID] = r
	}

	for _, service := range doc.Services {
		if service.Disabled {
			// A disabled service must not reappear: "I don't use Discord" is not
			// "delete my Discord config", and switching it on by accident is a
			// traffic change nobody asked for.
			if rule, ok := rules[Slug(service.Name)]; ok && rule.Enabled {
				t.Errorf("service %q is disabled in v1 but enabled after migration", service.Name)
			}
			continue
		}
		rule, ok := rules[Slug(service.Name)]
		if !ok {
			t.Errorf("service %q has no v2 rule", service.Name)
			continue
		}
		if !rule.Enabled {
			t.Errorf("service %q is enabled in v1 but disabled after migration", service.Name)
		}
		want := v1Actions(service)
		got := v2Actions(rule)
		if len(want) != len(got) {
			t.Errorf("service %q: chain length %d -> %d\n v1: %v\n v2: %v", service.Name, len(want), len(got), want, got)
			continue
		}
		for i := range want {
			if want[i] != got[i] {
				t.Errorf("service %q: step %d is %q in v1 and %q in v2\n full v1: %v\n full v2: %v",
					service.Name, i, want[i], got[i], want, got)
			}
		}
	}
}

// v1Actions flattens a service's chain into comparable action names, in order.
func v1Actions(service LegacyService) []string {
	var out []string
	for _, step := range service.Chain {
		switch step.Class {
		case strategy.ClassDirect:
			out = append(out, "direct")
		case strategy.ClassVPN, strategy.ClassEmergency:
			out = append(out, "vpn:"+step.StrategyID)
		case strategy.ClassZapret:
			name := step.StrategyID
			if name == "" {
				name = configv2.AutoStrategy
			}
			out = append(out, "desync:"+name)
		}
	}
	return out
}

func v2Actions(rule configv2.Rule) []string {
	var out []string
	for _, step := range rule.Chain {
		switch {
		case step.Action.Desync != nil:
			out = append(out, "desync:"+step.Action.Desync.Strategy)
		case step.Action.VPN != nil:
			out = append(out, "vpn:"+step.Action.VPN.Pool)
		case step.Action.Direct:
			out = append(out, "direct")
		}
	}
	return out
}

// Priority must carry over EXACTLY, because in v1 it is the emission order for
// first-match routing: a migrated rule that kept its chain but moved in priority
// changes which of two overlapping services claims a domain.
func TestMigrationPreservesPriority(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "r5s-production.yaml"))
	if err != nil {
		t.Skip("production example not available")
	}
	doc, err := ParseLegacyDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for _, r := range migration.Rules {
		byID[r.ID] = r.Priority
	}
	for _, service := range doc.Services {
		id := Slug(service.Name)
		got, ok := byID[id]
		if !ok {
			t.Errorf("service %q has no rule, so its priority is lost", service.Name)
			continue
		}
		if got != service.Priority {
			t.Errorf("service %q: priority %d -> %d; first-match routing depends on it", service.Name, service.Priority, got)
		}
	}
}

// Everything migration cannot express must be REPORTED. Silence here is the
// dangerous outcome: a lost field looks identical to a field nobody set.
func TestMigrationReportsEverythingItCannotExpress(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "r5s-production.yaml"))
	if err != nil {
		t.Skip("production example not available")
	}
	doc, err := ParseLegacyDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := MigrateToV2(doc, func(string) (string, error) { return configv2.AutoStrategy, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(migration.Warnings) == 0 {
		t.Fatal("the real config surely has fields v2 cannot express; none were reported")
	}
	// A warning must be LOCATABLE — it has to name the thing to go and fix, whether that
	// is a rule or a document-level section. A warning that says what changed without
	// saying where is a note, and notes are what the operator learns to skip.
	locatable := []string{`rule "`, "declared the", "desync instance", "dns", "fakeip", "override", "categories:", "subscription", "multiplex", "utls"}
	for _, w := range migration.Warnings {
		found := false
		for _, needle := range locatable {
			if strings.Contains(w, needle) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warning does not say what it is about: %q", w)
		}
	}
	t.Logf("%d rule(s), %d warning(s)", len(migration.Rules), len(migration.Warnings))
}
