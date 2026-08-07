package zaptune

import (
	"reflect"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

func rec(id string, cls strategycat.TargetClass) strategycat.Recipe {
	return strategycat.Recipe{ID: id, TargetClass: cls, NfqwsArgs: []string{"--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"}}
}

var catalog = []strategycat.Recipe{
	rec("disc1", strategycat.ClassDiscordTCP),
	rec("gen1", strategycat.ClassGeneralTLS),
	rec("quic1", strategycat.ClassQUIC),
	rec("game1", strategycat.ClassGames),
}

func TestCandidateRecipesByClass(t *testing.T) {
	gaming := registry.Service{Name: "gaming-battlenet", Profile: "gaming"}
	got := CandidateRecipes(gaming, catalog)
	// gaming -> general_tls + games (not discord, not quic)
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if !reflect.DeepEqual(ids, []string{"gen1", "game1"}) {
		t.Errorf("gaming candidates = %v, want [gen1 game1]", ids)
	}

	discord := registry.Service{Name: "discord", Profile: "voice"}
	got = CandidateRecipes(discord, catalog)
	ids = nil
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if !reflect.DeepEqual(ids, []string{"disc1", "gen1"}) {
		t.Errorf("discord candidates = %v, want [disc1 gen1]", ids)
	}
}

func TestBlockForKeepsServiceWhole(t *testing.T) {
	svc := registry.Service{Name: "gaming-battlenet", Domains: []string{"battle.net", "blizzard.com", "bnetcdn.com"}}
	block := BlockFor(svc, rec("gen1", strategycat.ClassGeneralTLS), svc.Domains, nil)
	// the ONE block must carry ALL the service's domains — never a per-domain split.
	if block.Service != "gaming-battlenet" {
		t.Errorf("block service = %q", block.Service)
	}
	if !reflect.DeepEqual(block.Domains, svc.Domains) {
		t.Errorf("block domains = %v, want all %v (service stays whole)", block.Domains, svc.Domains)
	}
}

func TestPromoteToRecipe(t *testing.T) {
	svc := registry.Service{Name: "discord", Profile: "voice"}
	args := []string{"--dpi-desync=multisplit", "--dpi-desync-split-pos=2"}
	rec, ok := PromoteToRecipe(svc, "gen-abc123", args)
	if !ok {
		t.Fatal("voice service should promote (discord_tcp has a filter)")
	}
	if rec.ID != "gen-abc123" || rec.Provenance != "generated" || rec.TargetClass != strategycat.ClassDiscordTCP {
		t.Errorf("recipe meta = %+v", rec)
	}
	// scoped: filter + {{DOMAINS}} + the rendered desync args, in order.
	want := []string{"--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=multisplit", "--dpi-desync-split-pos=2"}
	if !reflect.DeepEqual(rec.NfqwsArgs, want) {
		t.Errorf("args = %v, want %v", rec.NfqwsArgs, want)
	}
}

func TestPromoteToRecipeRejects(t *testing.T) {
	// gaming -> primary class general_tls HAS a filter, so it promotes; an empty
	// id or no args must not.
	g := registry.Service{Name: "gaming-x", Profile: "gaming"}
	if _, ok := PromoteToRecipe(g, "", []string{"--dpi-desync=fake"}); ok {
		t.Error("empty id must not promote")
	}
	if _, ok := PromoteToRecipe(g, "gen-x", nil); ok {
		t.Error("no args must not promote")
	}
}

// Raw UDP carries no SNI, so voice media cannot be selected by domain at all.
// Requiring {{DOMAINS}} therefore refused the one recipe that fixes Discord voice
// — Flowseal's own ALT12 block — while admitting far broader domain-scoped rules.
func TestPortAndL7ScopeCountsAsScoped(t *testing.T) {
	voice := strategycat.Recipe{
		ID: "flowseal-discord-stun",
		NfqwsArgs: []string{
			"--filter-udp=19294-19344,50000-50100",
			"--filter-l7=discord,stun",
			"--dpi-desync=fake",
			"--dpi-desync-repeats=6",
		},
	}
	if !recipeRenderable(voice) {
		t.Error("a 152-port profile with an L7 filter is a targeted rule and must render")
	}

	// The bound is what keeps the old protection: a games-class profile spanning
	// most of the port space would desync traffic nobody asked about.
	global := strategycat.Recipe{ID: "global", NfqwsArgs: []string{
		"--filter-udp=1024-65535", "--filter-l7=stun", "--dpi-desync=fake",
	}}
	if recipeRenderable(global) {
		t.Error("1024-65535 is not a scope, it is the internet")
	}

	// Ports without a protocol check are not enough either.
	noL7 := strategycat.Recipe{ID: "no-l7", NfqwsArgs: []string{
		"--filter-udp=50000-50100", "--dpi-desync=fake",
	}}
	if recipeRenderable(noL7) {
		t.Error("a port range with no --filter-l7 must still be refused")
	}

	// And the domain path is untouched.
	domain := strategycat.Recipe{ID: "d", NfqwsArgs: []string{
		"--filter-tcp=80,443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake",
	}}
	if !recipeRenderable(domain) {
		t.Error("domain-scoped recipes must still render")
	}
}
