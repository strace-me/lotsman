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
	block := BlockFor(svc, rec("gen1", strategycat.ClassGeneralTLS), svc.Domains)
	// the ONE block must carry ALL the service's domains — never a per-domain split.
	if block.Service != "gaming-battlenet" {
		t.Errorf("block service = %q", block.Service)
	}
	if !reflect.DeepEqual(block.Domains, svc.Domains) {
		t.Errorf("block domains = %v, want all %v (service stays whole)", block.Domains, svc.Domains)
	}
}
