package subscription

import (
	"context"
	"errors"
	"io"
	"net"
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

// fakeSOCKS5 is a minimal no-auth SOCKS5 CONNECT server for tests. It accepts
// one connection, performs the RFC 1928 handshake, then writes body as if it
// were the tunneled upstream's response and closes. It records that it was hit.
type fakeSOCKS5 struct {
	ln     net.Listener
	body   string
	hitCh  chan struct{}
	closed chan struct{}
}

func newFakeSOCKS5(t *testing.T, body string) *fakeSOCKS5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSOCKS5{ln: ln, body: body, hitCh: make(chan struct{}, 1), closed: make(chan struct{})}
	go s.serve()
	return s
}

func (s *fakeSOCKS5) addr() string { return s.ln.Addr().String() }

func (s *fakeSOCKS5) serve() {
	defer close(s.closed)
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	select {
	case s.hitCh <- struct{}{}:
	default:
	}

	// Greeting: VER, NMETHODS, METHODS...
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, int(hdr[1]))); err != nil {
		return
	}
	conn.Write([]byte{0x05, 0x00}) // no-auth chosen

	// CONNECT request: VER, CMD, RSV, ATYP, ADDR, PORT.
	rh := make([]byte, 4)
	if _, err := io.ReadFull(conn, rh); err != nil {
		return
	}
	var alen int
	switch rh[3] {
	case 0x01:
		alen = 4
	case 0x04:
		alen = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return
		}
		alen = int(l[0])
	default:
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, alen+2)); err != nil { // addr + port
		return
	}
	// Success reply, BND = 0.0.0.0:0.
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Drain the client's HTTP request, then write a canned HTTP/1.1 response
	// carrying body as the subscription payload.
	buf := make([]byte, 4096)
	conn.Read(buf)
	resp := "HTTP/1.1 200 OK\r\nContent-Length: " +
		itoa(len(s.body)) + "\r\nConnection: close\r\n\r\n" + s.body
	conn.Write([]byte(resp))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestHTTPFetcherProxy(t *testing.T) {
	const payload = "vless://u@9.9.9.9:443#viaproxy"
	srv := newFakeSOCKS5(t, payload)
	defer srv.ln.Close()

	f := NewHTTPFetcherProxy(srv.addr())
	// Target host is arbitrary and never resolved by us — the proxy "connects"
	// it. Use a TEST-NET host so a misconfigured client can't accidentally reach
	// the real internet.
	got, err := f.Fetch(context.Background(), "http://198.51.100.7/sub")
	if err != nil {
		t.Fatalf("fetch through proxy: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("got %q, want %q", got, payload)
	}
	select {
	case <-srv.hitCh:
	default:
		t.Fatal("proxy listener was never hit — fetch did not go through SOCKS5")
	}
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

func TestFetchHost(t *testing.T) {
	cases := []struct {
		url      string
		wantHost string
		wantOK   bool
	}{
		{"https://panel.example/s/TOKEN", "panel.example", true},
		{"https://panel.example:8443/s/TOKEN", "panel.example", true},
		{"http://1.2.3.4:9000/sub", "1.2.3.4", true},
		// Inline node share-links are the node itself, never a fetch host.
		{"hysteria2://pw@192.0.2.12:443?sni=x", "", false},
		{"vless://uuid@host:443", "", false},
	}
	for _, c := range cases {
		host, ok := FetchHost(Declaration{URL: c.url})
		if host != c.wantHost || ok != c.wantOK {
			t.Errorf("FetchHost(%q) = (%q,%v), want (%q,%v)", c.url, host, ok, c.wantHost, c.wantOK)
		}
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

func TestManagerEmptyFetchIsError(t *testing.T) {
	// acme-style flake: HTTP 200 with an empty body. The fetched sub must
	// surface an error (for the anti-churn guard) and contribute 0 nodes, while
	// a sibling good fetched sub still yields its node and an inline node URL
	// still yields its single node with no error.
	ff := fakeFetcher{data: map[string][]byte{
		"sub://acme": []byte("   \n\t "), // whitespace-only -> empty
		"sub://good":  []byte("vless://u@1.1.1.1:443#a"),
	}}
	decls := []Declaration{
		{Name: "acme", URL: "sub://acme", Format: FormatV2rayPlain, Tags: []string{"normal"}, Enabled: true},
		{Name: "good", URL: "sub://good", Format: FormatV2rayPlain, Tags: []string{"normal"}, Enabled: true},
		{Name: "inline", URL: "hysteria2://pw@2.2.2.2:443?sni=x&obfs=salamander", Format: FormatSingleURL, Tags: []string{"normal"}, Enabled: true},
	}

	nodes, errs := NewManager(ff).Load(context.Background(), decls)
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1 (the empty acme sub): %v", len(errs), errs)
	}
	// The good fetched sub and the inline node both survive.
	if _, ok := findNode(nodes, "1.1.1.1"); !ok {
		t.Errorf("good sub node 1.1.1.1 missing: %v", nodes)
	}
	if _, ok := findNode(nodes, "2.2.2.2"); !ok {
		t.Errorf("inline node 2.2.2.2 missing: %v", nodes)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2 (good + inline)", len(nodes))
	}
}

func TestManagerZeroNodeFetchIsError(t *testing.T) {
	// A non-empty body that parses to zero nodes (no valid scheme lines) is also
	// an error for a fetched sub, not a silent empty pull.
	ff := fakeFetcher{data: map[string][]byte{
		"sub://garbage": []byte("vless://u@1.1.1.1:443#a\nthis is not a node line"),
		"sub://nodes":   []byte("not-a-url\nalso-not-a-url\n"),
	}}
	decls := []Declaration{
		{Name: "nodes", URL: "sub://nodes", Format: FormatV2rayPlain, Enabled: true},
	}
	nodes, errs := NewManager(ff).Load(context.Background(), decls)
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1 (zero-node sub): %v", len(errs), errs)
	}
	if len(nodes) != 0 {
		t.Fatalf("got %d nodes, want 0", len(nodes))
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
