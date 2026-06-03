package dataplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ErrSelectorNotFound is returned by SetSelector when the selector does not
// exist in the running config (HTTP 404). For a DirectOnly service this is
// expected — it has no per-service selector and is routed direct by a route
// rule — so the direct executor treats it as success rather than an error.
var ErrSelectorNotFound = errors.New("clash: selector not found")

// ClashClient talks to sing-box's Clash-API-compatible endpoint. In M0 it is
// used to confirm a url-test pool is reachable before routing traffic to it;
// node selection within the pool is sing-box's job (url-test). Manual node
// pinning (SetSelector) is here for later use by recovery/affinity logic.
type ClashClient struct {
	Base   string // e.g. http://127.0.0.1:9090
	Secret string // clash-api secret, optional
	Client *http.Client
}

// NewClashClient builds a client with a short timeout.
func NewClashClient(base, secret string) *ClashClient {
	return &ClashClient{
		Base:   base,
		Secret: secret,
		Client: &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *ClashClient) do(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	return c.Client.Do(req)
}

// ProxyInfo is the subset of a Clash /proxies/{name} record Lotsman uses.
type ProxyInfo struct {
	Type string   `json:"type"` // Hysteria2 | URLTest | Selector | Direct ...
	Now  string   `json:"now"`  // group selectors: the currently chosen member
	All  []string `json:"all"`  // group selectors: member tags
}

// Proxy fetches one proxy/group record.
func (c *ClashClient) Proxy(ctx context.Context, name string) (ProxyInfo, error) {
	var info ProxyInfo
	resp, err := c.do(ctx, http.MethodGet, "/proxies/"+name)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("clash: proxy %q status %d", name, resp.StatusCode)
	}
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

// NodeDelay actively probes one node via the Clash API (GET
// /proxies/{name}/delay) and returns its latency in ms. A non-200 reply or a
// missing delay means the node failed the test (dead/unreachable) — returned as
// an error so the caller can rank it as down. This is how Lotsman gets a live
// per-node health signal that url-test's lazy interval misses.
func (c *ClashClient) NodeDelay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error) {
	path := fmt.Sprintf("/proxies/%s/delay?timeout=%d&url=%s", name, timeout.Milliseconds(), url.QueryEscape(testURL))
	resp, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var body struct {
		Delay   int    `json:"delay"`
		Message string `json:"message"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || body.Delay == 0 {
		msg := body.Message
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return 0, fmt.Errorf("clash: node %q delay failed: %s", name, msg)
	}
	return body.Delay, nil
}

// EnsurePool returns nil if the named proxy group exists and is queryable.
func (c *ClashClient) EnsurePool(ctx context.Context, group string) error {
	resp, err := c.do(ctx, http.MethodGet, "/proxies/"+group)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clash: group %q status %d", group, resp.StatusCode)
	}
	return nil
}

// SetSelector points a selector group at target (PUT /proxies/{selector}).
// This is how Lotsman switches the active path on the R5S: all tproxy traffic
// flows through sing-box, and flipping the selector reroutes a service between
// a VPN pool and direct without restarting sing-box. Idempotent.
func (c *ClashClient) SetSelector(ctx context.Context, selector, target string) error {
	body, _ := json.Marshal(map[string]string{"name": target})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.Base+"/proxies/"+selector, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Clash returns 204 on success; 400 means target not in the selector; 404
	// means the selector itself does not exist (a DirectOnly service).
	if resp.StatusCode == http.StatusNotFound {
		return ErrSelectorNotFound
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clash: set selector %q -> %q: status %d", selector, target, resp.StatusCode)
	}
	return nil
}
