package singbox

import (
	"strings"
	"testing"
)

func TestRemRuleSetSourceArmed(t *testing.T) {
	got := string(RemRuleSetSource([]string{"YouTube.com", "googlevideo.com", "googlevideo.com"}, []string{"142.250.0.0/15", "8.8.8.8/32"}))
	for _, want := range []string{
		`"version": 2`,
		`"domain_suffix"`, `"googlevideo.com"`, `"youtube.com"`, // lowercased + deduped
		`"ip_cidr"`, `"8.8.8.8/32"`, `"142.250.0.0/15"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("armed rule_set missing %q in:\n%s", want, got)
		}
	}
}

func TestRemRuleSetSourceDisarmedEmpty(t *testing.T) {
	got := string(RemRuleSetSource(nil, nil))
	if !strings.Contains(got, `"rules": []`) {
		t.Errorf("disarmed rule_set must have empty rules (matches nothing):\n%s", got)
	}
}

func TestRemRuleSetSourceDeterministic(t *testing.T) {
	a := string(RemRuleSetSource([]string{"b.com", "a.com"}, []string{"2.2.2.2/32", "1.1.1.1/32"}))
	b := string(RemRuleSetSource([]string{"a.com", "b.com"}, []string{"1.1.1.1/32", "2.2.2.2/32"}))
	if a != b {
		t.Error("output must be deterministic (sorted) regardless of input order")
	}
}
