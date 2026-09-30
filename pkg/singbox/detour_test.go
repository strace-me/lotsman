package singbox

import (
	"encoding/json"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
)

func detouredConfig(t *testing.T, detours map[string]string) map[string]any {
	t.Helper()
	nodes := []subscription.Node{
		mustParse(t, "trojan://pw@1.2.3.4:443#n1", subscription.FormatSingleURL),
		mustParse(t, "trojan://pw@1.2.3.5:443#n2", subscription.FormatSingleURL),
		mustParse(t, "trojan://pw@1.2.3.6:443#n3", subscription.FormatSingleURL),
	}
	memberships := map[string][]subscription.Node{
		"outer":    {nodes[0], nodes[1]},
		"underlay": {nodes[2]},
		"plain":    {nodes[0]},
	}
	svc := registry.Service{Name: "youtube"}
	opts := DefaultOptions()
	opts.Detours = detours
	res, err := Generate([]registry.Service{svc}, nil, nodes, memberships, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.JSON, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func detourOutboundsByTag(t *testing.T, cfg map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	raw, _ := cfg["outbounds"].([]any)
	for _, o := range raw {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		if tag, ok := m["tag"].(string); ok {
			out[tag] = m
		}
	}
	return out
}

// The underlay is a real outbound the detoured nodes point at, and the detoured
// node is a SEPARATE outbound from the plain one — otherwise a node used by both a
// detoured and a plain pool would carry the detour into the plain pool's traffic
// and route it somewhere nobody asked for.
func TestDetourProducesItsOwnNodeOutbound(t *testing.T) {
	cfg := detouredConfig(t, map[string]string{"outer": "underlay"})
	obs := detourOutboundsByTag(t, cfg)

	var detoured int
	for tag, ob := range obs {
		if d, ok := ob["detour"].(string); ok && d == "underlay" {
			detoured++
			if ob["type"] != "trojan" {
				t.Errorf("detoured outbound %q is a %v, not a copy of the node", tag, ob["type"])
			}
		}
	}
	if detoured != 2 {
		t.Errorf("detoured outbounds = %d, want 2 (the two nodes of the detoured pool)", detoured)
	}
	// The underlay group itself must exist, or the detour names a tag nothing owns.
	if _, ok := obs["underlay"]; !ok {
		t.Error("the underlay pool was not emitted, so the detour points at a tag that does not exist")
	}
	// A node ALSO in a plain pool keeps a plain outbound for that pool, and the
	// detoured one is that same tag plus a suffix. The tags are derived from the
	// node, not from its ID, so the relation is what is asserted, not a literal.
	var plainForDetoured string
	for tag := range obs {
		if i := len(tag) - len("~underlay"); i > 0 && tag[i:] == "~underlay" {
			plainForDetoured = tag[:i]
			break
		}
	}
	if plainForDetoured == "" {
		t.Fatal("no detoured outbound carries the <plain>~underlay tag shape")
	}
	if _, ok := obs[plainForDetoured]; !ok {
		t.Errorf("the plain variant %q of a shared node was not emitted, so the plain "+
			"pool's traffic would inherit the detour", plainForDetoured)
	}
}

// Empty detours must change nothing at all. A composition feature that perturbs the
// config of a deployment not using it would show up as reconcile churn — and this
// project has already paid for a restart-per-diff incident (LOT-1).
func TestNoDetoursMeansNoChange(t *testing.T) {
	cfg := detouredConfig(t, nil)
	for tag, ob := range detourOutboundsByTag(t, cfg) {
		if _, ok := ob["detour"]; ok {
			t.Errorf("outbound %q carries a detour with none configured", tag)
		}
	}
}

// The underlay group must be built from PLAIN outbounds. Keying the variant lookup
// on the underlay's name instead of on the pool's own detour made the underlay
// assemble itself out of the detoured variants, so it pointed at nodes that dial
// out through it.
//
// sing-box ACCEPTED that. `check` validates shape, not reachability, so the config
// was green and the pool resolved to nothing — the "everything reports healthy and
// nothing works" shape this project keeps paying for.
func TestTheUnderlayGroupIsNotBuiltFromItsOwnDetouredVariants(t *testing.T) {
	cfg := detouredConfig(t, map[string]string{"outer": "underlay"})
	obs := detourOutboundsByTag(t, cfg)

	underlay, ok := obs["underlay"]
	if !ok {
		t.Fatal("the underlay group was not emitted")
	}
	members, _ := underlay["outbounds"].([]any)
	if len(members) == 0 {
		t.Fatal("the underlay group is empty")
	}
	for _, m := range members {
		tag, _ := m.(string)
		if i := len(tag) - len("~underlay"); i > 0 && tag[i:] == "~underlay" {
			t.Errorf("the underlay group contains %q, a node that detours through the "+
				"underlay itself — the group would point at itself", tag)
		}
		if _, self := obs[tag]; !self {
			t.Errorf("underlay member %q is not an outbound in this config", tag)
		}
	}
	// And the detoured pool really does use the variants, or the feature does nothing.
	outer, ok := obs["outer"]
	if !ok {
		t.Fatal("the detoured pool was not emitted")
	}
	used := false
	for _, m := range outer["outbounds"].([]any) {
		if tag, _ := m.(string); len(tag) > len("~underlay") && tag[len(tag)-len("~underlay"):] == "~underlay" {
			used = true
		}
	}
	if !used {
		t.Error("the detoured pool is made of plain outbounds, so the detour does nothing")
	}
}
