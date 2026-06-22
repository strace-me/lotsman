package subscription

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// isNodeURL reports whether s is a node share-link (its own scheme) rather than
// an HTTP endpoint to fetch a subscription from.
func isNodeURL(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range []string{"hysteria2://", "hy2://", "vless://", "trojan://", "ss://", "vmess://", "anytls://", "tuic://", "shadowtls://", "wireguard://", "wg://"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// FetchHost returns the bare hostname that a declaration is fetched FROM, and
// ok=false for an inline node share-link (which is the node itself, never
// fetched). It is the host that has to be reachable to pull the subscription, so
// callers can route that host through the VPN tunnel (LOT-28) instead of the
// unreliable direct path. The port is stripped — route matching is by host.
func FetchHost(d Declaration) (string, bool) {
	if isNodeURL(d.URL) {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(d.URL))
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	return u.Hostname(), true
}

// Declaration is one entry from subscriptions.yaml: where to fetch, how to
// parse, and which tags to stamp on every node it yields. Tags are how a
// subscription feeds pool membership (e.g. tags: [emergency] -> emergency_pool).
type Declaration struct {
	Name    string   `yaml:"name"`
	URL     string   `yaml:"url"`
	Format  Format   `yaml:"format"`
	Tags    []string `yaml:"tags"`
	Enabled bool     `yaml:"enabled"`
}

// Fetcher retrieves raw subscription bytes. An interface so the Manager is
// testable without network and so manual/single-URL sources can be faked.
type Fetcher interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// HTTPFetcher fetches over HTTP(S).
type HTTPFetcher struct{ Client *http.Client }

// NewHTTPFetcher builds a fetcher with a sane timeout (direct egress).
func NewHTTPFetcher() *HTTPFetcher {
	return &HTTPFetcher{Client: &http.Client{Timeout: 15 * time.Second}}
}

// NewHTTPFetcherProxy builds a fetcher whose HTTP client dials through the
// given no-auth SOCKS5 proxy (host:port) — i.e. the sing-box socks inbound that
// probes use. This lets a subscription fetch egress via the VPN tunnel, which
// matters when the upstream host is unreliable to reach directly from the RU
// network. socksAddr is the proxy's host:port.
func NewHTTPFetcherProxy(socksAddr string) *HTTPFetcher {
	d := &socks5Dialer{proxy: socksAddr, timeout: 15 * time.Second}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &HTTPFetcher{Client: &http.Client{Timeout: 15 * time.Second, Transport: tr}}
}

func (f *HTTPFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	b, _, err := f.fetch(ctx, url)
	return b, err
}

// FetchWithHeaders is Fetch plus the response headers, so the Manager can read
// the Subscription-Userinfo quota/expiry header (LOT-7). A sibling method (not on
// the Fetcher interface) keeps the change zero-ripple: only the subscription
// Manager opts in via a type assertion; aggregate/flowseal and the test fakes are
// untouched.
func (f *HTTPFetcher) FetchWithHeaders(ctx context.Context, url string) ([]byte, http.Header, error) {
	return f.fetch(ctx, url)
}

func (f *HTTPFetcher) fetch(ctx context.Context, url string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("subscription fetch %s: status %d", url, resp.StatusCode)
	}
	const maxBytes = 8 << 20 // 8 MiB cap; subscriptions are tiny
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if len(buf) > maxBytes {
			return nil, nil, fmt.Errorf("subscription %s: body exceeds %d bytes", url, maxBytes)
		}
		if rerr != nil {
			break
		}
	}
	return buf, resp.Header, nil
}

// Manager turns a set of declarations into one merged, deduplicated node list.
type Manager struct {
	fetcher Fetcher

	mu       sync.Mutex
	userinfo map[string]Userinfo // per-declaration quota/expiry from the last fetch (LOT-7)
}

// NewManager builds a Manager over the given fetcher.
func NewManager(f Fetcher) *Manager {
	return &Manager{fetcher: f}
}

// headerFetcher is the optional capability a fetcher implements to expose
// response headers (HTTPFetcher does). The Manager uses it to read the
// Subscription-Userinfo quota/expiry header; fakes that only implement Fetcher
// simply don't surface userinfo.
type headerFetcher interface {
	FetchWithHeaders(ctx context.Context, url string) ([]byte, http.Header, error)
}

// Userinfo returns a copy of the per-declaration quota/expiry captured on the
// last Load. Safe for concurrent reads (e.g. a metrics scrape) while Load runs.
func (m *Manager) Userinfo() map[string]Userinfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Userinfo, len(m.userinfo))
	for k, v := range m.userinfo {
		out[k] = v
	}
	return out
}

func (m *Manager) setUserinfo(name string, ui Userinfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.userinfo == nil {
		m.userinfo = map[string]Userinfo{}
	}
	m.userinfo[name] = ui
}

// Load fetches and parses every enabled declaration, stamps each node with the
// declaration's tags and source, and merges the results, deduplicating by node
// ID (tags from all occurrences are unioned). A failing subscription is
// skipped with an error returned in errs — one dead upstream does not sink the
// whole load. Output order is stable (sorted by node ID).
func (m *Manager) Load(ctx context.Context, decls []Declaration) (nodes []Node, errs []error) {
	merged := map[string]*Node{}
	var order []string

	for _, d := range decls {
		if !d.Enabled {
			continue
		}
		// A node share-link (hysteria2://, vless://, …) IS the node — declare it
		// inline without an HTTP fetch. Lets one manual node (e.g. the single paid
		// hysteria2 key) live in the config without a subscription endpoint.
		var raw []byte
		inline := isNodeURL(d.URL)
		if inline {
			raw = []byte(d.URL)
		} else {
			var fetched []byte
			var err error
			// Capture the Subscription-Userinfo quota/expiry header when the fetcher
			// can expose headers (LOT-7); fall back to the plain Fetch otherwise.
			if hf, ok := m.fetcher.(headerFetcher); ok {
				var hdr http.Header
				fetched, hdr, err = hf.FetchWithHeaders(ctx, d.URL)
				if err == nil {
					if ui, ok := ParseUserinfo(hdr.Get("Subscription-Userinfo")); ok {
						m.setUserinfo(d.Name, ui)
					}
				}
			} else {
				fetched, err = m.fetcher.Fetch(ctx, d.URL)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("subscription %q: fetch: %w", d.Name, err))
				continue
			}
			// A flaky upstream (e.g. vpn-a) can return HTTP 200 with an empty or
			// whitespace-only body. For a FETCHED subscription that is an error,
			// not "0 nodes" — surface it so the reconcile anti-churn guard catches
			// it. Inline proto:// declarations are the node itself, never empty.
			if len(strings.TrimSpace(string(fetched))) == 0 {
				errs = append(errs, fmt.Errorf("subscription %q: fetch: empty body", d.Name))
				continue
			}
			raw = fetched
		}
		parsed, err := Parse(raw, d.Format, d.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("subscription %q: parse: %w", d.Name, err))
			continue
		}
		// A fetched subscription that parses to ZERO nodes (garbage body that
		// happened to parse cleanly, all entries skipped, etc.) is likewise an
		// error, not a silent empty pull. Inline declarations always yield their
		// one node, so this only bites fetched subs.
		if !inline && len(parsed) == 0 {
			errs = append(errs, fmt.Errorf("subscription %q: parse: yielded 0 nodes", d.Name))
			continue
		}
		for i := range parsed {
			n := parsed[i]
			n.Tags = append([]string(nil), d.Tags...)
			if existing, ok := merged[n.ID]; ok {
				existing.Tags = unionTags(existing.Tags, n.Tags)
				continue
			}
			merged[n.ID] = &n
			order = append(order, n.ID)
		}
	}

	sort.Strings(order)
	nodes = make([]Node, 0, len(order))
	for _, id := range order {
		nodes = append(nodes, *merged[id])
	}
	return nodes, errs
}

// socks5Dialer dials TCP through a no-auth SOCKS5 proxy (RFC 1928, CONNECT).
// It mirrors pkg/dataplane.socks5Dialer; it is inlined here rather than reused
// because that type is unexported (exporting it would touch another package).
type socks5Dialer struct {
	proxy   string // host:port of the SOCKS5 server
	timeout time.Duration
}

// DialContext connects to addr through the proxy and returns the tunneled conn.
func (s *socks5Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("socks5: unsupported network %q", network)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("socks5: bad port %q: %w", portStr, err)
	}

	conn, err := (&net.Dialer{Timeout: s.timeout}).DialContext(ctx, "tcp", s.proxy)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	// Greeting: VER=5, one method, METHOD=0 (no auth).
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(conn, rep); err != nil {
		conn.Close()
		return nil, err
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5: no-auth rejected (%#x %#x)", rep[0], rep[1])
	}

	// CONNECT request. Prefer IP literal ATYP, else domain (proxy resolves).
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 0x01)
			req = append(req, v4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			conn.Close()
			return nil, fmt.Errorf("socks5: hostname too long")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// Reply: VER, REP, RSV, ATYP, BND.ADDR, BND.PORT.
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		conn.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5: connect failed (rep=%d)", head[1])
	}
	var alen int
	switch head[3] {
	case 0x01:
		alen = 4
	case 0x04:
		alen = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			conn.Close()
			return nil, err
		}
		alen = int(l[0])
	default:
		conn.Close()
		return nil, fmt.Errorf("socks5: bad reply atyp %d", head[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, alen+2)); err != nil {
		conn.Close()
		return nil, err
	}

	conn.SetDeadline(time.Time{}) // hand a clean conn to the caller
	return conn, nil
}

func unionTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, t := range append(append([]string(nil), a...), b...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
