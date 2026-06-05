package zaptune

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// recipe is a tiny domains-only recipe builder for tests.
func recipe(id string, class strategycat.TargetClass, args ...string) strategycat.Recipe {
	return strategycat.Recipe{ID: id, TargetClass: class, NfqwsArgs: args}
}

func svcWithDomains(name, profile string, domains ...string) registry.Service {
	return registry.Service{Name: name, Profile: profile, Domains: domains}
}

func TestComposeCoversAllServices(t *testing.T) {
	cat := []strategycat.Recipe{
		recipe("disc-tcp-1", strategycat.ClassDiscordTCP, "--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"),
		recipe("tls-1", strategycat.ClassGeneralTLS, "--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=split2"),
	}
	services := []registry.Service{
		svcWithDomains("discord", "voice", "discord.media", "dis.gd"),
		svcWithDomains("gaming-epic", "gaming", "epicgames.com"),
	}
	p := Compose(services, cat, FirstPicker)
	if !p.Covered {
		t.Fatalf("expected covered, uncovered=%v", p.Uncovered)
	}
	// Two --new blocks, each with its service's domains.
	if n := strings.Count(strings.Join(p.Args, " "), "--new"); n != 2 {
		t.Errorf("want 2 --new blocks, got %d in %v", n, p.Args)
	}
	joined := strings.Join(p.Args, " ")
	if !strings.Contains(joined, "discord.media,dis.gd") {
		t.Errorf("discord domains not inlined: %v", p.Args)
	}
	if !strings.Contains(joined, "epicgames.com") {
		t.Errorf("gaming domains not inlined: %v", p.Args)
	}
	if p.Chosen["discord"] != "disc-tcp-1" {
		t.Errorf("discord recipe = %q, want disc-tcp-1", p.Chosen["discord"])
	}
}

func TestComposeUncoveredKeepsExisting(t *testing.T) {
	// Only a discord-class recipe exists; gaming has no usable candidate -> not
	// covered -> caller keeps the existing whole-config (alt12).
	cat := []strategycat.Recipe{
		recipe("disc-tcp-1", strategycat.ClassDiscordTCP, "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"),
	}
	services := []registry.Service{
		svcWithDomains("discord", "voice", "discord.media"),
		svcWithDomains("gaming-epic", "gaming", "epicgames.com"), // general_tls/games — none in cat
	}
	p := Compose(services, cat, FirstPicker)
	if p.Covered {
		t.Errorf("should NOT be covered when a service has no recipe: %v", p)
	}
	if len(p.Uncovered) != 1 || p.Uncovered[0] != "gaming-epic" {
		t.Errorf("uncovered = %v, want [gaming-epic]", p.Uncovered)
	}
	if p.Args != nil {
		t.Errorf("no args when not covered, got %v", p.Args)
	}
}

func TestComposeSkipsUnfillablePlaceholders(t *testing.T) {
	// A recipe needing {{IPSET}} is NOT usable in 10b (only {{DOMAINS}} filled),
	// so it is not a candidate and the only-such-recipe service is uncovered.
	cat := []strategycat.Recipe{
		recipe("ipset-1", strategycat.ClassGeneralTLS, "--ipset={{IPSET}}", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"),
	}
	p := Compose([]registry.Service{svcWithDomains("dev", "general", "github.com")}, cat, FirstPicker)
	if p.Covered {
		t.Error("recipe with {{IPSET}} must not be usable in 10b")
	}
}

func TestComposeServiceWithoutDomainsUncovered(t *testing.T) {
	cat := []strategycat.Recipe{recipe("tls-1", strategycat.ClassGeneralTLS, "--hostlist-domains={{DOMAINS}}", "--dpi-desync=split2")}
	// rule_set-only service (no inline domains) — nfqws can't read .srs, so it is
	// not domain-renderable -> uncovered.
	svc := registry.Service{Name: "web", Profile: "general", RuleSets: []string{"geosite-ru-blocked"}}
	p := Compose([]registry.Service{svc}, cat, FirstPicker)
	if p.Covered {
		t.Error("service with no inline domains must be uncovered (nfqws can't read rule_sets)")
	}
}

func TestKBPickerRanksBySuccessColdFallsToFirst(t *testing.T) {
	cands := []strategycat.Recipe{
		recipe("a", strategycat.ClassGeneralTLS, "--hostlist-domains={{DOMAINS}}"),
		recipe("b", strategycat.ClassGeneralTLS, "--hostlist-domains={{DOMAINS}}"),
		recipe("c", strategycat.ClassGeneralTLS, "--hostlist-domains={{DOMAINS}}"),
	}
	svc := svcWithDomains("web", "general", "x.com")

	// Cold KB: all equal -> first candidate (catalog order), like FirstPicker.
	cold := KBPicker(func(string, string) float64 { return 0.5 })
	if r, _ := cold(svc, cands); r.ID != "a" {
		t.Errorf("cold picker should take first candidate, got %q", r.ID)
	}

	// b has the best learned success -> picked despite not being first.
	scores := map[string]float64{"a": 0.4, "b": 0.9, "c": 0.7}
	warm := KBPicker(func(_, id string) float64 { return scores[id] })
	if r, _ := warm(svc, cands); r.ID != "b" {
		t.Errorf("warm picker should take best-success b, got %q", r.ID)
	}

	// Tie at the top -> earliest candidate wins (stable).
	tie := KBPicker(func(_, id string) float64 {
		if id == "a" || id == "b" {
			return 0.9
		}
		return 0.1
	})
	if r, _ := tie(svc, cands); r.ID != "a" {
		t.Errorf("tie should keep earliest (a), got %q", r.ID)
	}
}

func TestRecipesFromDefinitions(t *testing.T) {
	defs := []strategy.Definition{
		// classified general_tls -> recipe with tcp filter + domains + technique.
		{ID: "disc-1", Class: strategy.ClassZapret, TargetClass: "general_tls", NFQWSArgs: []string{"--dpi-desync=fake", "--dpi-desync-repeats=6"}},
		// unclassified -> skipped (no TargetClass).
		{ID: "disc-2", Class: strategy.ClassZapret, NFQWSArgs: []string{"--dpi-desync=split2"}},
		// unscopable class (games) -> skipped.
		{ID: "disc-3", Class: strategy.ClassZapret, TargetClass: "games", NFQWSArgs: []string{"--dpi-desync=fake"}},
		// script-backed (no args) -> skipped even if classified.
		{ID: "alt12", Class: strategy.ClassZapret, TargetClass: "general_tls"},
	}
	got := RecipesFromDefinitions(defs)
	if len(got) != 1 || got[0].ID != "disc-1" {
		t.Fatalf("only the classified+scopable+technique def should convert, got %v", got)
	}
	r := got[0]
	if r.TargetClass != strategycat.ClassGeneralTLS || r.Provenance != "discovered" {
		t.Errorf("recipe meta wrong: %+v", r)
	}
	joined := strings.Join(r.NfqwsArgs, " ")
	if !strings.Contains(joined, "--filter-tcp=80,443") || !strings.Contains(joined, "--hostlist-domains={{DOMAINS}}") || !strings.Contains(joined, "--dpi-desync=fake") {
		t.Errorf("converted recipe missing filter/domains/technique: %v", r.NfqwsArgs)
	}
	// The converted recipe must be domain-renderable (composable in 10b).
	if !usableForDomains(svcWithDomains("web", "general", "x.com"), r) {
		t.Error("converted discovered recipe should be domain-renderable")
	}
}

func TestComposeEmptyServicesNotCovered(t *testing.T) {
	p := Compose(nil, []strategycat.Recipe{recipe("tls-1", strategycat.ClassGeneralTLS, "--hostlist-domains={{DOMAINS}}")}, FirstPicker)
	if p.Covered || p.Args != nil {
		t.Errorf("no zapret-active services -> not covered, got %v", p)
	}
}
