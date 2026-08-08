package zaptune

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// The catalogue holds 28 recipes written for YouTube and the YouTube rule could
// not see one of them: the streaming profile asked for quic and general_tls, and
// ClassYouTube was in neither. Five candidates survived the composer's filters,
// the lane tried all five over and over, all five lost — and the search read as
// exhausted with twenty-seven purpose-built recipes unreachable in the catalogue.
func TestYouTubeCanReachTheRecipesWrittenForIt(t *testing.T) {
	all := strategycat.Load()
	yt := registry.Service{
		Name: "youtube", Profile: "streaming",
		ProbeTarget: "https://www.youtube.com/generate_204",
		Domains:     []string{"youtube.com"},
	}
	var yc int
	for _, r := range CandidateRecipes(yt, all) {
		if r.TargetClass == strategycat.ClassYouTube {
			yc++
		}
	}
	if yc == 0 {
		t.Fatal("not one youtube-class recipe is offered to the youtube rule")
	}
	if got := len(UsableCandidates(yt, all)); got < 20 {
		t.Errorf("usable candidates for youtube = %d, want the purpose-built ones included", got)
	}
	// And the classes it already had must not be lost — general_tls is where
	// flowseal-general-fake-multisplit-664-max lives, the recipe that has actually
	// carried this service.
	var general bool
	for _, r := range CandidateRecipes(yt, all) {
		if r.ID == "flowseal-general-fake-multisplit-664-max" {
			general = true
		}
	}
	if !general {
		t.Error("adding the youtube class dropped the general_tls ones")
	}
}

// A streaming rule that is not youtube keeps working, and discord keeps its own
// classes: the change must widen one rule's reach, not reshuffle everyone's.
func TestOtherProfilesKeepTheirClasses(t *testing.T) {
	all := strategycat.Load()
	dc := registry.Service{Name: "discord", Profile: "voice",
		ProbeTarget: "https://discord.com/api/v9/gateway", Domains: []string{"discord.com"}}
	var discordTCP int
	for _, r := range CandidateRecipes(dc, all) {
		if r.TargetClass == strategycat.ClassDiscordTCP {
			discordTCP++
		}
		if r.TargetClass == strategycat.ClassYouTube {
			t.Errorf("discord was offered a youtube recipe: %s", r.ID)
		}
	}
	if discordTCP == 0 {
		t.Error("discord lost its own class")
	}
}
