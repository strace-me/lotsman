package zapret

import (
	"strings"
	"testing"
)

// Without this rule the tuner cannot exist: the sandbox and the production table
// share a hook, so a probe would be desynced by the candidate AND then by the
// incumbent, and would measure neither.
func TestProductionTableLetsTheSandboxProbeThrough(t *testing.T) {
	got := GenerateNft([]Instance{{Name: "main", QNum: 200, Connbytes: 12,
		Capture: Capture{TCP: []string{"443"}}}}, NftOptions{Table: "inet zapret", WAN: "eth0"})

	skip := strings.Index(got, "meta mark 0x4554 return")
	queue := strings.Index(got, "queue num 200")
	if skip < 0 {
		t.Fatalf("production table does not let the sandbox mark through:\n%s", got)
	}
	// Order is the whole property: appended after the queue rules it never
	// matches, and everything would look fine while measuring nothing.
	if queue >= 0 && skip > queue {
		t.Errorf("the skip rule sits AFTER the queue rules, so it never matches:\n%s", got)
	}
}
