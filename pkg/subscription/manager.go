package subscription

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
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

// NewHTTPFetcher builds a fetcher with a sane timeout.
func NewHTTPFetcher() *HTTPFetcher {
	return &HTTPFetcher{Client: &http.Client{Timeout: 15 * time.Second}}
}

func (f *HTTPFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription fetch %s: status %d", url, resp.StatusCode)
	}
	const maxBytes = 8 << 20 // 8 MiB cap; subscriptions are tiny
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if len(buf) > maxBytes {
			return nil, fmt.Errorf("subscription %s: body exceeds %d bytes", url, maxBytes)
		}
		if rerr != nil {
			break
		}
	}
	return buf, nil
}

// Manager turns a set of declarations into one merged, deduplicated node list.
type Manager struct {
	fetcher Fetcher
}

// NewManager builds a Manager over the given fetcher.
func NewManager(f Fetcher) *Manager {
	return &Manager{fetcher: f}
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
		if isNodeURL(d.URL) {
			raw = []byte(d.URL)
		} else {
			fetched, err := m.fetcher.Fetch(ctx, d.URL)
			if err != nil {
				errs = append(errs, fmt.Errorf("subscription %q: fetch: %w", d.Name, err))
				continue
			}
			raw = fetched
		}
		parsed, err := Parse(raw, d.Format, d.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("subscription %q: parse: %w", d.Name, err))
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
