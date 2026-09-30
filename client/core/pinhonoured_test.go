package core

import (
	"log/slog"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/registry"
)

func pinnedCore(pin string, autoBump bool) *Core {
	v2 := &configv2.Configuration{DataSources: map[string]configv2.DataSource{
		"runetfreedom": {Type: "v2ray-srs", Repo: "r/r", Pin: pin, Refresh: configv2.Duration(time.Hour)},
	}}
	return &Core{reg: &registry.Registry{Services: map[string]registry.Service{
		"youtube": {Name: "youtube", RuleSets: []string{"geosite-youtube"}}}},
		log:  slog.New(slog.DiscardHandler),
		opts: Options{RuleSetDir: "/tmp/rs", V2Config: v2, RuleSetAutoBump: autoBump}}
}

// A pin that is ignored is worse than no pin, because it looks like a decision and
// is not one. The client resolved `releases/latest` every period even with a pin and
// died on a 403 for a tag it already had.
func TestPinIsHonouredWithoutAutobump(t *testing.T) {
	up, _, err := pinnedCore("202609302013", false).ruleSetUpdater(nil)
	if err != nil || up == nil {
		t.Fatalf("no updater: %v", err)
	}
	if up.AutoBump {
		t.Error("AutoBump is on under a pin — the updater will hit the API and can 403")
	}
	if up.Pin != "202609302013" {
		t.Errorf("pin = %q", up.Pin)
	}
}

// No pin means track latest, which is the whole point of leaving it empty.
func TestNoPinMeansTrackLatest(t *testing.T) {
	up, _, err := pinnedCore("", false).ruleSetUpdater(nil)
	if err != nil || up == nil {
		t.Fatalf("no updater: %v", err)
	}
	if !up.AutoBump {
		t.Error("an unpinned source does not track latest — updates are silently dead")
	}
}

// The operator override has to actually override, or "pin as a floor while riding
// latest" is not expressible.
func TestAutobumpFlagOverridesThePin(t *testing.T) {
	up, _, err := pinnedCore("202609302013", true).ruleSetUpdater(nil)
	if err != nil || up == nil {
		t.Fatalf("no updater: %v", err)
	}
	if !up.AutoBump {
		t.Error("-ruleset-autobump had no effect on a pinned source")
	}
}
