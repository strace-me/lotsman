package singbox

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// LOT-38: a node with a short/empty ID must not panic on n.ID[:6], and a normal
// (>=6 char) ID is still truncated to the first 6 (tag format unchanged).
func TestOutboundTagShortIDNoPanic(t *testing.T) {
	for _, id := range []string{"", "ab", "abc12"} {
		got := outboundTag(subscription.Node{DisplayName: "DE Node", Protocol: "vless", ID: id})
		if !strings.HasSuffix(got, "-"+id) {
			t.Errorf("id=%q: tag %q must end with -%s (and must not panic)", id, got, id)
		}
	}
	if got := outboundTag(subscription.Node{DisplayName: "DE Node", Protocol: "vless", ID: "0123456789abcdef"}); !strings.HasSuffix(got, "-012345") {
		t.Errorf("long id: tag %q must end with -012345 (first 6)", got)
	}
}

func TestPickUTLSDiversityWithConsistency(t *testing.T) {
	pool := []string{"chrome", "firefox", "edge", "safari"}
	// Consistent: same node id -> same fingerprint across calls (stable per node).
	if pickUTLS("node-A", "", pool) != pickUTLS("node-A", "", pool) {
		t.Error("same node id must map to the same fingerprint")
	}
	// Diverse: across a set of ids, more than one fingerprint is used.
	seen := map[string]bool{}
	for _, id := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8"} {
		fp := pickUTLS(id, "", pool)
		inPool := false
		for _, p := range pool {
			if p == fp {
				inPool = true
			}
		}
		if !inPool {
			t.Errorf("picked %q not in pool", fp)
		}
		seen[fp] = true
	}
	if len(seen) < 2 {
		t.Errorf("pool draw not diverse across the fleet: only %v", seen)
	}
	// Fallback: empty pool -> the single default; both empty -> "".
	if pickUTLS("x", "chrome", nil) != "chrome" {
		t.Error("empty pool should fall back to the single fingerprint")
	}
	if pickUTLS("x", "", nil) != "" {
		t.Error("no pool and no single -> off")
	}
}
