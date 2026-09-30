package core

import (
	"log/slog"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/registry"
)

// The period and the repo come from the DOCUMENT, and that is the whole reason the
// client's lists can be current: before this, the client fetched once and never again,
// so a list that went stale upstream stayed stale on the box while the router re-read
// it. A knob the operator has to remember to pass is a second answer to a question the
// config already answers.
func TestRuleSetPeriodAndRepoComeFromTheDocument(t *testing.T) {
	v2 := &configv2.Configuration{DataSources: map[string]configv2.DataSource{
		"runetfreedom": {
			Type: "v2ray-srs", Repo: "someone/fork", Pin: "v2026-09-01",
			Refresh: configv2.Duration(90 * time.Minute),
		},
	}}
	reg := &registry.Registry{Services: map[string]registry.Service{
		"youtube": {Name: "youtube", RuleSets: []string{"geosite-youtube"}},
	}}
	c := &Core{reg: reg, log: slog.New(slog.DiscardHandler), opts: Options{
		RuleSetDir: "/tmp/rs", SingboxBin: "sing-box", V2Config: v2,
	}}
	up, every, err := c.ruleSetUpdater(nil)
	if err != nil {
		t.Fatal(err)
	}
	if up == nil {
		t.Fatal("a document with a fetchable data source produced no updater")
	}
	if up.Repo != "someone/fork" {
		t.Errorf("repo = %q, want the DOCUMENT's fork, not the built-in default", up.Repo)
	}
	if up.Pin != "v2026-09-01" {
		t.Errorf("pin = %q, want the document's", up.Pin)
	}
	if every != 90*time.Minute {
		t.Errorf("period = %v, want the document's 90m", every)
	}
	if len(up.Required) != 1 || up.Required[0] != "geosite-youtube" {
		t.Errorf("required = %v, want the tags the services reference", up.Required)
	}
}

// A document with no data source must fall back to the flags rather than doing
// nothing in silence: a config that looks fed and never refreshes is the failure.
func TestV1FallsBackToTheFlags(t *testing.T) {
	reg := &registry.Registry{Services: map[string]registry.Service{
		"youtube": {Name: "youtube", RuleSets: []string{"geosite-youtube"}},
	}}
	c := &Core{reg: reg, log: slog.New(slog.DiscardHandler), opts: Options{
		RuleSetDir: "/tmp/rs", RuleSetRepo: "flags/repo", RuleSetRefresh: 2 * time.Hour,
	}}
	up, every, err := c.ruleSetUpdater(nil)
	if err != nil || up == nil {
		t.Fatalf("the v1 flag path produced nothing: %v", err)
	}
	if up.Repo != "flags/repo" || every != 2*time.Hour {
		t.Errorf("repo=%q every=%v, want the flags", up.Repo, every)
	}
}

// Nothing referenced, nothing to fetch. An updater with an empty Required list would
// happily download a bundle and plan a swap of nothing.
func TestNoRuleSetsMeansNoUpdater(t *testing.T) {
	c := &Core{reg: &registry.Registry{Services: map[string]registry.Service{}},
		log: slog.New(slog.DiscardHandler), opts: Options{RuleSetRepo: "flags/repo"}}
	up, _, err := c.ruleSetUpdater(nil)
	if err != nil {
		t.Fatal(err)
	}
	if up != nil {
		t.Error("an updater was built for a config that references no rule-sets")
	}
}
