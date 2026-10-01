package zapret

import (
	"context"
	"strings"
	"testing"
)

// The real defect, as a test.
//
// `ip rule del` removes the FIRST rule matching the selector it was given. Deleting
// without pref means "del fwmark 0x40000000 lookup main" could take out an unrelated
// rule that merely also says `lookup main` — and the live table has one:
// `400: from all to 217.144.170.170 lookup main`, a route for a single address that
// somebody installed for a reason.
//
// Every rule deletion must name its pref, or a sandbox teardown can silently destroy
// a rule it does not own.
func TestRuleDeletionsNameTheirPref(t *testing.T) {
	r := &recRunner{}
	s := &Sandbox{Run: r}
	ctx := context.Background()

	// installLocked renders both isolation rules; removeProbeRoute tears one down.
	_ = s.installLocked(ctx)
	s.removeProbeRoute(ctx)

	seen := 0
	for _, line := range r.cmds {
		if !strings.Contains(line, "rule del") {
			continue
		}
		seen++
		if !strings.Contains(line, "pref") {
			t.Errorf("rule deletion without pref could remove a foreign rule: %q", line)
		}
	}
	if seen == 0 {
		t.Fatal("no rule deletion was issued — the test does not exercise the path")
	}
}
