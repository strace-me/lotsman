package subscription

import (
	"context"
	"errors"
	"testing"
)

// fakeFetcher serves canned bytes per URL, or an error.
type fakeFetcher struct {
	data map[string][]byte
	err  map[string]error
}

func (f fakeFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	if e, ok := f.err[url]; ok {
		return nil, e
	}
	if d, ok := f.data[url]; ok {
		return d, nil
	}
	return nil, errors.New("no such url")
}

func findNode(nodes []Node, server string) (Node, bool) {
	for _, n := range nodes {
		if n.Server == server {
			return n, true
		}
	}
	return Node{}, false
}

func TestManagerInlineNodeNoFetch(t *testing.T) {
	// The fetcher has NO data, so any HTTP fetch errors. A node share-link URL
	// must be used inline (no fetch) and still yield the node.
	m := NewManager(fakeFetcher{})
	decls := []Declaration{{
		Name:    "fastvpn",
		URL:     "hysteria2://pw@192.0.2.12:443?sni=nl3.example&obfs=salamander",
		Format:  FormatSingleURL,
		Tags:    []string{"normal"},
		Enabled: true,
	}}
	nodes, errs := m.Load(context.Background(), decls)
	if len(errs) != 0 {
		t.Fatalf("inline node should not fetch/error: %v", errs)
	}
	n, ok := findNode(nodes, "192.0.2.12")
	if !ok {
		t.Fatalf("inline hysteria2 node not loaded: %v", nodes)
	}
	if !n.Caps.UDPNative {
		t.Errorf("hysteria2 node should be udp_native")
	}
}

func TestManagerTagsAndMerges(t *testing.T) {
	ff := fakeFetcher{data: map[string][]byte{
		"sub://normal":    []byte("vless://u@1.1.1.1:443#a\nhysteria2://p@2.2.2.2:443#b"),
		"sub://emergency": []byte("hysteria2://p@2.2.2.2:443#b"), // 2.2.2.2 appears in both
	}}
	decls := []Declaration{
		{Name: "normal", URL: "sub://normal", Format: FormatV2rayPlain, Tags: []string{"normal"}, Enabled: true},
		{Name: "emerg", URL: "sub://emergency", Format: FormatV2rayPlain, Tags: []string{"emergency", "slow"}, Enabled: true},
		{Name: "off", URL: "sub://normal", Format: FormatV2rayPlain, Tags: []string{"x"}, Enabled: false},
	}

	nodes, errs := NewManager(ff).Load(context.Background(), decls)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// 1.1.1.1 (one sub) + 2.2.2.2 (deduped across two subs) = 2 nodes.
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}

	// The shared node should carry the union of tags from both subscriptions.
	shared, ok := findNode(nodes, "2.2.2.2")
	if !ok {
		t.Fatal("missing shared node 2.2.2.2")
	}
	wantTags := map[string]bool{"normal": true, "emergency": true, "slow": true}
	if len(shared.Tags) != len(wantTags) {
		t.Fatalf("shared tags = %v, want union %v", shared.Tags, wantTags)
	}
	for _, tg := range shared.Tags {
		if !wantTags[tg] {
			t.Errorf("unexpected tag %q", tg)
		}
	}

	// The normal-only node carries only the normal tag.
	only, _ := findNode(nodes, "1.1.1.1")
	if len(only.Tags) != 1 || only.Tags[0] != "normal" {
		t.Errorf("1.1.1.1 tags = %v, want [normal]", only.Tags)
	}
}

func TestManagerSkipsFailedSubscription(t *testing.T) {
	ff := fakeFetcher{
		data: map[string][]byte{"sub://ok": []byte("vless://u@1.1.1.1:443#a")},
		err:  map[string]error{"sub://dead": errors.New("connection refused")},
	}
	decls := []Declaration{
		{Name: "dead", URL: "sub://dead", Format: FormatV2rayPlain, Enabled: true},
		{Name: "ok", URL: "sub://ok", Format: FormatV2rayPlain, Tags: []string{"normal"}, Enabled: true},
	}

	nodes, errs := NewManager(ff).Load(context.Background(), decls)
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1", len(errs))
	}
	// The healthy subscription still yields its node.
	if len(nodes) != 1 || nodes[0].Server != "1.1.1.1" {
		t.Fatalf("got %v, want one node 1.1.1.1", nodes)
	}
}
