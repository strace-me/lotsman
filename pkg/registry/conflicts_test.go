package registry

import "testing"

func TestDetectConflicts(t *testing.T) {
	services := []Service{
		{Name: "youtube", RuleSets: []string{"geosite-youtube"}, Domains: []string{"ytimg.com", "shared.example"}},
		{Name: "other", Domains: []string{"shared.example"}, IPs: []string{"66.22.192.0/18"}},
		{Name: "voice", IPs: []string{"66.22.200.0/24"}},     // inside other's /18
		{Name: "web", RuleSets: []string{"geosite-youtube"}}, // same rule-set as youtube
	}
	got := DetectConflicts(services)

	// Index by kind+value for assertions.
	find := func(kind, value string) *Conflict {
		for i := range got {
			if got[i].Kind == kind && got[i].Value == value {
				return &got[i]
			}
		}
		return nil
	}

	if c := find("domain", "shared.example"); c == nil {
		t.Error("missing domain conflict for shared.example")
	} else if len(c.Services) != 2 || c.Services[0] != "other" || c.Services[1] != "youtube" {
		t.Errorf("shared.example services = %v, want [other youtube]", c.Services)
	}
	if find("rule_set", "geosite-youtube") == nil {
		t.Error("missing rule_set conflict for geosite-youtube (youtube + web)")
	}
	if c := find("ip", "66.22.192.0/18 overlaps 66.22.200.0/24"); c == nil {
		t.Error("missing ip overlap other/voice")
	}
}

func TestDetectConflictsClean(t *testing.T) {
	services := []Service{
		{Name: "a", Domains: []string{"a.com"}, IPs: []string{"10.0.0.0/8"}},
		{Name: "b", Domains: []string{"b.com"}, IPs: []string{"192.168.0.0/16"}},
	}
	if got := DetectConflicts(services); len(got) != 0 {
		t.Errorf("expected no conflicts, got %v", got)
	}
}

// A service listing the same IP twice (or two of its own ranges nesting) is not
// a conflict — only cross-service overlaps are.
func TestDetectConflictsIgnoresSelfOverlap(t *testing.T) {
	services := []Service{
		{Name: "discord", IPs: []string{"66.22.192.0/18", "66.22.200.0/24"}},
	}
	if got := DetectConflicts(services); len(got) != 0 {
		t.Errorf("self-overlap must not be a conflict, got %v", got)
	}
}
