package singbox

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// observe counts bytes per outbound TAG; pools ranks by Node.ID. If the two are
// passed at each other directly, every lookup misses, the pool keeps all its
// nodes, and the defect comes back as "I set speedtest_top and nothing happened".
func TestThroughputByNodeIDRekeysTagsOntoNodes(t *testing.T) {
	nodes := []subscription.Node{
		{ID: "alpha", Protocol: "vless", Server: "a.example", Port: 443},
		{ID: "beta", Protocol: "trojan", Server: "b.example", Port: 443},
	}
	alphaTag, ok := NodeTag(nodes[0])
	if !ok {
		t.Skip("node has no outbound tag in this build")
	}
	betaTag, ok := NodeTag(nodes[1])
	if !ok {
		t.Skip("node has no outbound tag in this build")
	}

	got := ThroughputByNodeID(nodes, map[string]float64{alphaTag: 900, betaTag: 120})
	if got["alpha"] != 900 || got["beta"] != 120 {
		t.Fatalf("re-keyed = %v, want alpha=900 beta=120", got)
	}
	// The keys must be node IDs, because that is what pools ranks on.
	if _, wrong := got[alphaTag]; wrong && alphaTag != "alpha" {
		t.Errorf("keyed by tag %q instead of node id", alphaTag)
	}
}

func TestThroughputByNodeIDDropsWhatIsNotANode(t *testing.T) {
	// "direct" and the selector groups come out of the same connection table. A
	// measurement on one of them must not become a pool member: that would rank a
	// bulk pool on the box's own local exit.
	nodes := []subscription.Node{{ID: "alpha", Protocol: "vless", Server: "a.example", Port: 443}}
	got := ThroughputByNodeID(nodes, map[string]float64{
		"direct":       5000,
		"sel-youtube":  4000,
		"vpn_url_test": 3000,
		"":             100,
		"nonexistent":  2000,
	})
	if len(got) != 0 {
		t.Errorf("non-node measurements became members: %v", got)
	}
}

func TestThroughputByNodeIDIgnoresUnmeasuredAndZero(t *testing.T) {
	// "not measured" and "measured as nothing" are opposite facts: the first is an
	// idle exit, the second a frozen one. Passing zero through as a rate would
	// make the second look like the first, and a frozen exit would rank as
	// merely idle instead of as carrying nothing.
	nodes := []subscription.Node{{ID: "alpha", Protocol: "vless", Server: "a.example", Port: 443}}
	tag, ok := NodeTag(nodes[0])
	if !ok {
		t.Skip("node has no outbound tag in this build")
	}
	if got := ThroughputByNodeID(nodes, map[string]float64{tag: 0}); len(got) != 0 {
		t.Errorf("a zero measurement became a rate: %v", got)
	}
	if got := ThroughputByNodeID(nodes, map[string]float64{tag: -5}); len(got) != 0 {
		t.Errorf("a negative measurement became a rate: %v", got)
	}
	if got := ThroughputByNodeID(nodes, nil); len(got) != 0 {
		t.Errorf("no measurements produced members: %v", got)
	}
}
