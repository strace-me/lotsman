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
