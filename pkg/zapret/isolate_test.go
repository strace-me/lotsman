package zapret

import (
	"strings"
	"testing"
)

func TestIsolateTableTouchesNothingUnmarked(t *testing.T) {
	got := GenerateIsolateNft(IsolateOptions{
		Table: "inet lotsman_tune", WAN: "eth0", QNum: 201,
		TCP: []string{"80", "443"}, UDP: []string{"443"}, Bytes: 12, Prio: -200,
	})
	// The first statement in the chain must be the escape hatch. If a rule ever
	// precedes it, real traffic reaches a candidate strategy nobody vetted — on
	// the household's only uplink.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	var firstRule string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "table") || strings.HasPrefix(l, "chain") || strings.HasPrefix(l, "type filter") {
			continue
		}
		firstRule = l
		break
	}
	if firstRule != "meta mark != 0x4554 return" {
		t.Errorf("first rule is %q, want the unmarked-traffic escape", firstRule)
	}
	if !strings.Contains(got, "queue num 201 bypass") {
		t.Errorf("candidate queue missing:\n%s", got)
	}
	// It must never name the production queue.
	if strings.Contains(got, "queue num 200") {
		t.Errorf("sandbox references the production queue:\n%s", got)
	}
}

// Both tables run on the same hook, so without this rule in the PRODUCTION table
// the probe is desynced twice — by the candidate and then by the incumbent — and
// measures neither.
func TestProductionSkipRuleMatchesTheSandboxMark(t *testing.T) {
	if got := ProductionSkipRule(0); got != "meta mark 0x4554 return" {
		t.Errorf("skip rule = %q", got)
	}
	if got := ProductionSkipRule(0x99); !strings.Contains(got, "0x99") {
		t.Errorf("custom mark ignored: %q", got)
	}
}
