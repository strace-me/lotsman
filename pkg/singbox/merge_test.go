package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// existingConfig is a trimmed stand-in for the router's hand-tuned config: a vpn
// selector over a hysteria2 url-test pool plus two concrete hy2 nodes, a direct
// outbound, and a route block we must not disturb.
const existingConfig = `{
  "log": {"level": "info"},
  "inbounds": [{"type": "tproxy", "tag": "tproxy-in"}],
  "outbounds": [
    {"type": "selector", "tag": "vpn", "outbounds": ["vpn-pool", "vpn-c-nl", "vpn-d"], "default": "vpn-pool"},
    {"type": "urltest", "tag": "vpn-pool", "outbounds": ["vpn-c-nl", "vpn-d"]},
    {"type": "hysteria2", "tag": "vpn-c-nl", "server": "1.2.3.4", "server_port": 443},
    {"type": "hysteria2", "tag": "vpn-d", "server": "5.6.7.8", "server_port": 443},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {"final": "direct", "rules": [
    {"ip_is_private": true, "outbound": "direct"},
    {"rule_set": ["geoip-cloudflare", "geosite-cloudflare"], "outbound": "vpn"},
    {"rule_set": ["geosite-youtube"], "outbound": "vpn"}
  ]}
}`

func routeRules(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	route, _ := cfg["route"].(map[string]any)
	raw, _ := route["rules"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func vlessNode(server string, port int, name string) subscription.Node {
	n := subscription.Node{
		Protocol:    subscription.ProtoVLESS,
		Server:      server,
		Port:        port,
		DisplayName: name,
		Raw:         "vless://11111111-2222-3333-4444-555555555555@" + server + ":" + itoa(port) + "?type=tcp&security=tls&sni=" + server,
	}
	// mirror finalize() enough for outboundTag (needs ID) without exporting it.
	n.ID = idFor(server, port)
	return n
}

func itoa(i int) string { return strings.TrimSpace(jsonNum(i)) }
func jsonNum(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// idFor builds a >=6-char hex-ish id so outboundTag's n.ID[:6] slice is safe.
func idFor(server string, port int) string {
	s := server + itoa(port)
	for len(s) < 6 {
		s += "0"
	}
	return s
}

func parseOutbounds(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	raw, _ := cfg["outbounds"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func findOutbound(obs []map[string]any, tag string) map[string]any {
	for _, m := range obs {
		if m["tag"] == tag {
			return m
		}
	}
	return nil
}

func TestMergeAddsPoolsAndPreservesExisting(t *testing.T) {
	groups := []NodeGroup{
		{PoolName: "vpn-a-pool", Nodes: []subscription.Node{
			vlessNode("a.example.com", 8443, "AlmatyKZ"),
			vlessNode("b.example.com", 8443, "AmsterdamNL"),
		}},
	}
	res, err := Merge([]byte(existingConfig), groups, MergeOptions{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.NodeCount != 2 {
		t.Errorf("NodeCount = %d, want 2", res.NodeCount)
	}
	if len(res.Pools) != 1 || res.Pools[0] != "vpn-a-pool" {
		t.Errorf("Pools = %v, want [vpn-a-pool]", res.Pools)
	}

	obs := parseOutbounds(t, res.JSON)

	// hy2 nodes + vpn-pool + direct preserved.
	for _, tag := range []string{"vpn-c-nl", "vpn-d", "vpn-pool", "direct"} {
		if findOutbound(obs, tag) == nil {
			t.Errorf("preserved outbound %q missing after merge", tag)
		}
	}
	// vpn-a-pool group exists and is a urltest with 2 members.
	bp := findOutbound(obs, "vpn-a-pool")
	if bp == nil {
		t.Fatal("vpn-a-pool not added")
	}
	if bp["type"] != "urltest" {
		t.Errorf("vpn-a-pool type = %v, want urltest", bp["type"])
	}
	if mem, _ := bp["outbounds"].([]any); len(mem) != 2 {
		t.Errorf("vpn-a-pool members = %v, want 2", bp["outbounds"])
	}
	// vpn selector now includes vpn-a-pool AND still has its old members.
	sel := findOutbound(obs, "vpn")
	mem, _ := sel["outbounds"].([]any)
	got := map[string]bool{}
	for _, m := range mem {
		got[m.(string)] = true
	}
	for _, want := range []string{"vpn-pool", "vpn-c-nl", "vpn-d", "vpn-a-pool"} {
		if !got[want] {
			t.Errorf("vpn selector missing %q; has %v", want, mem)
		}
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	groups := []NodeGroup{{PoolName: "vpn-a-pool", Nodes: []subscription.Node{
		vlessNode("a.example.com", 8443, "AlmatyKZ"),
	}}}
	first, err := Merge([]byte(existingConfig), groups, MergeOptions{})
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	second, err := Merge(first.JSON, groups, MergeOptions{})
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	obs := parseOutbounds(t, second.JSON)

	// vpn-a-pool appears exactly once.
	count := 0
	for _, m := range obs {
		if m["tag"] == "vpn-a-pool" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("vpn-a-pool appears %d times after double merge, want 1", count)
	}
	// vpn selector lists vpn-a-pool exactly once.
	sel := findOutbound(obs, "vpn")
	mem, _ := sel["outbounds"].([]any)
	bp := 0
	for _, m := range mem {
		if m == "vpn-a-pool" {
			bp++
		}
	}
	if bp != 1 {
		t.Errorf("vpn selector lists vpn-a-pool %d times, want 1; has %v", bp, mem)
	}
}

func TestMergeErrorsWhenSelectorMissing(t *testing.T) {
	groups := []NodeGroup{{PoolName: "p", Nodes: []subscription.Node{vlessNode("a.example.com", 8443, "x")}}}
	_, err := Merge([]byte(existingConfig), groups, MergeOptions{SelectorTag: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want selector-not-found error, got %v", err)
	}
}

func TestMergePrimaryFallbackUDP(t *testing.T) {
	groups := []NodeGroup{{PoolName: "vpn-a-pool", Nodes: []subscription.Node{
		vlessNode("a.example.com", 8443, "AlmatyKZ"),
	}}}
	res, err := Merge([]byte(existingConfig), groups, MergeOptions{UDPMode: UDPPrimaryFallback})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// vpn-udp = [hy2 natives..., vless pools...]
	udp := findOutbound(parseOutbounds(t, res.JSON), "vpn-udp")
	if udp == nil {
		t.Fatal("vpn-udp group not created")
	}
	mem := udp["outbounds"].([]any)
	got := map[string]bool{}
	for _, m := range mem {
		got[m.(string)] = true
	}
	for _, want := range []string{"vpn-c-nl", "vpn-d", "vpn-a-pool"} {
		if !got[want] {
			t.Errorf("vpn-udp missing %q; has %v", want, mem)
		}
	}
	// native nodes come before the vless pool (fallback ordering).
	if mem[len(mem)-1] != "vpn-a-pool" {
		t.Errorf("expected vless pool last in vpn-udp, got %v", mem)
	}
	// a network:udp rule was inserted, pointing at vpn-udp, with the union of
	// the rule-sets that targeted vpn.
	rules := routeRules(t, res.JSON)
	var udpRule map[string]any
	for _, r := range rules {
		if r["outbound"] == "vpn-udp" {
			udpRule = r
		}
	}
	if udpRule == nil {
		t.Fatal("no network:udp rule for vpn-udp")
	}
	if udpRule["network"] != "udp" {
		t.Errorf("udp rule network = %v, want udp", udpRule["network"])
	}
	rs := map[string]bool{}
	for _, x := range udpRule["rule_set"].([]any) {
		rs[x.(string)] = true
	}
	for _, want := range []string{"geoip-cloudflare", "geosite-cloudflare", "geosite-youtube"} {
		if !rs[want] {
			t.Errorf("udp rule rule_set missing %q; has %v", want, udpRule["rule_set"])
		}
	}
}

func TestMergeSplitUDPExcludesVLESS(t *testing.T) {
	groups := []NodeGroup{{PoolName: "vpn-a-pool", Nodes: []subscription.Node{
		vlessNode("a.example.com", 8443, "AlmatyKZ"),
	}}}
	res, err := Merge([]byte(existingConfig), groups, MergeOptions{UDPMode: UDPSplit})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	udp := findOutbound(parseOutbounds(t, res.JSON), "vpn-udp")
	if udp == nil {
		t.Fatal("vpn-udp not created")
	}
	for _, m := range udp["outbounds"].([]any) {
		if m == "vpn-a-pool" {
			t.Errorf("split mode must not put VLESS pool in vpn-udp; has %v", udp["outbounds"])
		}
	}
}

func TestMergeUnifiedNoUDPGroup(t *testing.T) {
	groups := []NodeGroup{{PoolName: "vpn-a-pool", Nodes: []subscription.Node{
		vlessNode("a.example.com", 8443, "AlmatyKZ"),
	}}}
	res, err := Merge([]byte(existingConfig), groups, MergeOptions{UDPMode: UDPUnified})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if findOutbound(parseOutbounds(t, res.JSON), "vpn-udp") != nil {
		t.Error("unified mode must not create vpn-udp")
	}
	for _, r := range routeRules(t, res.JSON) {
		if r["network"] == "udp" {
			t.Errorf("unified mode must not add a network:udp rule: %v", r)
		}
	}
}

func TestMergeUDPRuleIdempotent(t *testing.T) {
	groups := []NodeGroup{{PoolName: "vpn-a-pool", Nodes: []subscription.Node{
		vlessNode("a.example.com", 8443, "AlmatyKZ"),
	}}}
	first, _ := Merge([]byte(existingConfig), groups, MergeOptions{})
	second, err := Merge(first.JSON, groups, MergeOptions{})
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	n := 0
	for _, r := range routeRules(t, second.JSON) {
		if r["outbound"] == "vpn-udp" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("network:udp rule appears %d times after double merge, want 1", n)
	}
}

func TestMergeSkipsEmptyGroup(t *testing.T) {
	groups := []NodeGroup{{PoolName: "empty-pool", Nodes: nil}}
	res, err := Merge([]byte(existingConfig), groups, MergeOptions{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Pools) != 0 {
		t.Errorf("empty group should add no pool, got %v", res.Pools)
	}
	if findOutbound(parseOutbounds(t, res.JSON), "empty-pool") != nil {
		t.Error("empty-pool should not be added")
	}
}
