package iplearn

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Source fetches a maintained CIDR set for a domain. Fetch returns the parsed
// CIDRs; malformed lines are skipped, not fatal.
type Source interface {
	Fetch(ctx context.Context, domain string) ([]*net.IPNet, error)
}

// HTTPDoer is the injectable HTTP seam (satisfied by *http.Client). Tests
// supply a fake so no network is touched.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// RekrytSource fetches per-domain CIDR4 lists from a rekryt/iplist mirror
// (default iplist.opencck.org) using the text format:
//
//	<BaseURL>?format=text&site=<domain>&data=cidr4
//
// The response body is newline-separated CIDRs (and/or bare IPs); blank lines
// and '#'/'//' comments are stripped.
type RekrytSource struct {
	// BaseURL is the query endpoint. Empty means DefaultRekrytBaseURL.
	BaseURL string
	// HTTP is the client used for requests. Required (inject *http.Client or a
	// fake); nil yields an error on Fetch.
	HTTP HTTPDoer
	// Data is the data= query value. Empty means "cidr4".
	Data string
}

// DefaultRekrytBaseURL is the hosted rekryt/iplist text endpoint.
const DefaultRekrytBaseURL = "https://russia.iplist.opencck.org/"

// NewRekrytSource builds a RekrytSource over the given HTTP client, defaulting
// the base URL and data parameter.
func NewRekrytSource(http HTTPDoer) *RekrytSource {
	return &RekrytSource{BaseURL: DefaultRekrytBaseURL, HTTP: http, Data: "cidr4"}
}

// Fetch retrieves and parses the CIDR list for domain.
func (s *RekrytSource) Fetch(ctx context.Context, domain string) ([]*net.IPNet, error) {
	if s.HTTP == nil {
		return nil, fmt.Errorf("iplearn: RekrytSource has no HTTP client")
	}
	base := s.BaseURL
	if base == "" {
		base = DefaultRekrytBaseURL
	}
	data := s.Data
	if data == "" {
		data = "cidr4"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("iplearn: bad base URL: %w", err)
	}
	q := u.Query()
	q.Set("format", "text")
	q.Set("site", domain)
	q.Set("data", data)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iplearn: %s: status %d", domain, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ParseCIDRList(body), nil
}

// ParseCIDRList extracts CIDRs from raw list bytes: one per line, '#'/'//'
// comments stripped, whitespace trimmed. A bare IP becomes a host route
// (/32, /128). Malformed lines are skipped. The result is deduplicated by
// containment (a /32 inside an included block is dropped).
func ParseCIDRList(raw []byte) []*net.IPNet {
	set := map[string]*net.IPNet{}
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n := parseCIDROrIP(line)
		if n == nil {
			continue
		}
		addToSet(set, n)
	}
	out := make([]*net.IPNet, 0, len(set))
	for _, n := range set {
		out = append(out, n)
	}
	sortNets(out)
	return out
}

// parseCIDROrIP parses a CIDR or a bare IP (widened to a host route),
// normalizing the IP to its 4-byte form for v4 so dedup/containment match
// across representations. Returns nil on malformed input.
func parseCIDROrIP(s string) *net.IPNet {
	if _, n, err := net.ParseCIDR(s); err == nil {
		if v4 := n.IP.To4(); v4 != nil {
			ones, _ := n.Mask.Size()
			return &net.IPNet{IP: v4, Mask: net.CIDRMask(ones, 32)}
		}
		return n
	}
	if ip := net.ParseIP(s); ip != nil {
		return hostCIDR(ip)
	}
	return nil
}
