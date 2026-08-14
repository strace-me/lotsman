// Package control is a thin, dependency-light client for a Lotsman service's
// control socket. Both the GUI window and the (separate) tray process talk to the
// service ONLY through this — the unix socket is the privilege boundary, so an
// unprivileged UI never links the engine. The DTOs below mirror the server's JSON
// contract (client/core.Report et al.) deliberately by value, not by import, so
// this package stays free of the daemon spine.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Report is the rich /status payload. It is a superset of the legacy
// {running, services}; unknown future fields are ignored on decode.
type Report struct {
	// Version is the build answering. A UI that cannot say which build it is
	// talking to makes every bug report and every rollback a guess.
	Version       string         `json:"version"`
	Running       bool           `json:"running"`
	Verdict       Verdict        `json:"verdict"`
	Network       Network        `json:"network"`
	Engines       []Engine       `json:"engines"`
	Fleet         Fleet          `json:"fleet"`
	Subscriptions []Subscription `json:"subscriptions"`
	Services      []Service      `json:"services"`
	// Notices are environment/configuration conditions worth showing — a tunnel that
	// protects less than it looks like it does. Not failures, and not alerts.
	Notices []Notice `json:"notices"`
	// Disabled names services the operator switched off. They carry no runtime
	// state — nothing probes or routes them — so they are names, not Services.
	Disabled []string `json:"disabled"`
}

// Notice is a condition the daemon wants the operator to see. Text is the whole
// explanation and is rendered verbatim: a UI keeping its own copy of the wording
// would drift from the daemon that actually evaluates the condition.
type Notice struct {
	Code     string   `json:"code"`
	Text     string   `json:"text"`
	Services []string `json:"services"`
}

// Verdict is the top-line coverage rollup. State is one of
// working | partial | not-working | down.
type Verdict struct {
	State   string `json:"state"`
	Working int    `json:"working"`
	Failing int    `json:"failing"`
	Broken  int    `json:"broken"`
	Total   int    `json:"total"`
}

// Network is the fingerprint of the network the live KB belongs to.
type Network struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Carrier string `json:"carrier"`
	IFace   string `json:"iface"`
	Roaming bool   `json:"roaming"`
}

// Engine is one process the service drives (lotsman | sing-box | nfqws), by
// measured liveness (running | stopped).
type Engine struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// Fleet separates the counts a single "nodes" number used to blur together: how
// many exits the subscriptions yielded (Total), how many distinct addresses those
// exits sit behind (Servers), and what each declared pool actually selected
// (Pools) — the last being the number a service is really choosing from.
type Fleet struct {
	Total   int            `json:"total"`
	Servers int            `json:"servers"`
	Pools   map[string]int `json:"pools"`
	// Nodes is every exit the subscriptions yielded. The service has emitted it for
	// a while; this DTO simply did not decode it, so the Nodes tab degraded to a
	// single number against the real backend while looking complete against the
	// mock — a fleet you are paying for and cannot see.
	Nodes []FleetNode `json:"nodes"`
}

// FleetNode is one exit as configured. No liveness: the client keeps none per
// node, and colouring a node green because it exists is the sort of unobserved
// claim this project keeps finding. Warm is a property of the POOL — it is kept
// continuously probed, so a failover lands on an already-hot node.
type FleetNode struct {
	Name     string   `json:"name"`
	Server   string   `json:"server"`
	Protocol string   `json:"protocol"`
	Country  string   `json:"country"`
	Source   string   `json:"source"`
	Pools    []string `json:"pools"`
	Warm     bool     `json:"warm"`
}

// Subscription is one subscription's quota/expiry.
type Subscription struct {
	Name            string  `json:"name"`
	UsedBytes       int64   `json:"usedBytes"`
	TotalBytes      int64   `json:"totalBytes"`
	FractionUsed    float64 `json:"fractionUsed"`
	DaysUntilExpire float64 `json:"daysUntilExpire"`
	Expired         bool    `json:"expired"`
	// ExpirySource: "provider" (from the Subscription-Userinfo header) or "manual"
	// (typed into the config). Empty when no date is known.
	ExpirySource string `json:"expirySource"`
}

// Service is one service's current state and the passive eye's ratios.
type Service struct {
	Service   string `json:"service"`
	State     string `json:"state"`
	Node      string `json:"node"`
	Fails     int    `json:"fails"`
	Broken    bool   `json:"broken"`
	Rung      int    `json:"rung"`
	RungClass string `json:"rungClass"`
	Engine    string `json:"engine"`
	Strategy  string `json:"strategy"`
	// StrategyPreset is the upstream's own name for the running recipe ("ALT12").
	StrategyPreset string  `json:"strategyPreset"`
	Requested      string  `json:"requested"`
	StalledRatio   float64 `json:"stalledRatio"`
	LeakRatio      float64 `json:"leakRatio"`
}

// Event is one brain rung-transition (the "история событий" surface).
type Event struct {
	Time          time.Time `json:"time"`
	Service       string    `json:"service"`
	FromPosition  int       `json:"from_position"`
	ToPosition    int       `json:"to_position"`
	State         string    `json:"state"`
	StrategyClass string    `json:"strategy_class"`
	StrategyID    string    `json:"strategy_id"`
	Reason        string    `json:"reason"`
}

// ConfigDoc is the service's config file: its path, the raw YAML (for the escape
// hatch), and Doc — the structured config the UI edits with forms. Doc is opaque
// here (json.RawMessage) so this package stays free of pkg/config; the frontend
// parses it.
type ConfigDoc struct {
	Path string          `json:"path"`
	YAML string          `json:"yaml"`
	Doc  json.RawMessage `json:"doc,omitempty"`
}

// Client talks to one control socket. It is safe for concurrent use.
type Client struct {
	http *http.Client
}

// New returns a client dialing the unix socket at path. Every request carries the
// given timeout so a wedged service cannot hang the UI.
func New(socketPath string) *Client {
	return &Client{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				if err != nil {
					return nil, explainDial(socketPath, err)
				}
				return conn, nil
			},
		},
	}}
}

// explainDial says what a failed connect to the control socket means for the
// person looking at it. The kernel's two words here — "permission denied" and "no
// such file or directory" — both render in a UI as "the service is broken", and
// one of them is usually wrong: a system service the session may not reach is
// running perfectly.
//
// The remedy is NOT the obvious one. "Log out and back in" is what everybody
// reaches for after a group change, and on a systemd desktop it does not work:
// `systemd --user` is per USER, not per session, so it outlives the logout and
// keeps the group set it was started with, and anything the app launcher starts
// inherits that. Observed on the ThinkPad the day the unit was installed — a full
// logout left the manager's groups unchanged. Killing the user manager, or a
// reboot, is what actually refreshes them.
func explainDial(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w — a service IS listening at %s but this session may not reach it. If Lotsman was just installed as a system service, this session predates the lotsman group. Logging out is NOT enough (systemd --user survives it and keeps the old groups): run `loginctl terminate-user $USER` from a text console, or reboot", err, path)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w — nothing is listening at %s (is the lotsman service running?)", err, path)
	}
	return err
}

// Status fetches the rich /status.
func (c *Client) Status(ctx context.Context) (Report, error) {
	var r Report
	err := c.getJSON(ctx, "/status", &r)
	return r, err
}

// Events fetches recent brain transitions newest-first. limit <= 0 = all
// retained; service "" = every service.
func (c *Client) Events(ctx context.Context, limit int, service string) ([]Event, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if service != "" {
		q.Set("service", service)
	}
	path := "/events"
	if e := q.Encode(); e != "" {
		path += "?" + e
	}
	var ev []Event
	err := c.getJSON(ctx, path, &ev)
	return ev, err
}

// Recheck forces an immediate probe of one service.
func (c *Client) Recheck(ctx context.Context, service string) error {
	return c.post(ctx, "/service/"+url.PathEscape(service)+"/recheck")
}

// SetServiceEnabled switches one service on or off. It is a config edit applied
// in place, not a runtime override, so it survives a restart and cannot be undone
// by the next reassert.
func (c *Client) SetServiceEnabled(ctx context.Context, service string, enabled bool) error {
	return c.postJSON(ctx, "/service/"+url.PathEscape(service)+"/enabled",
		map[string]bool{"enabled": enabled})
}

// Stop is the master OFF: it tears the service down and the process exits.
// SubRefresh is what one subscription yielded when it was re-fetched.
type SubRefresh struct {
	Name  string `json:"name"`
	Nodes int    `json:"nodes"`
	Err   string `json:"err,omitempty"`
}

// RefreshSubscriptions re-fetches subscriptions and applies the result. An empty
// name refreshes all of them.
//
// It returns what EACH one yielded, including the reason one produced nothing —
// the question this exists to answer. On 2026-08-12 a freshly added subscription
// contributed zero nodes and there was no way to ask why; the answer had already
// rotated out of a six-minute journald ring.
//
// Unlike SetConfig it does not rebuild the autonomy loop: it runs the same
// reconcile the refresh timer runs, so the tunnel is not torn down to ask a
// provider for its node list.
func (c *Client) RefreshSubscriptions(ctx context.Context, name string) ([]SubRefresh, error) {
	path := "/subscriptions/refresh"
	if name != "" {
		path = "/subscriptions/" + url.PathEscape(name) + "/refresh"
	}
	var out struct {
		Subscriptions []SubRefresh `json:"subscriptions"`
		ApplyError    string       `json:"apply_error"`
	}
	if err := c.postJSONInto(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	if out.ApplyError != "" {
		return out.Subscriptions, errors.New(out.ApplyError)
	}
	return out.Subscriptions, nil
}

func (c *Client) Stop(ctx context.Context) error {
	return c.post(ctx, "/stop")
}

// Config fetches the service's current config file (raw YAML + its path).
func (c *Client) Config(ctx context.Context) (ConfigDoc, error) {
	var d ConfigDoc
	err := c.getJSON(ctx, "/config", &d)
	return d, err
}

// ValidateConfig checks a candidate config without writing it; the returned error
// carries the parser message when the YAML is invalid.
func (c *Client) ValidateConfig(ctx context.Context, yaml string) error {
	return c.postJSON(ctx, "/config/validate", ConfigDoc{YAML: yaml})
}

// SetConfig validates + writes a raw-YAML config and applies it (the escape hatch).
// The service re-execs, so the socket briefly drops — expect to reconnect.
func (c *Client) SetConfig(ctx context.Context, yaml string) error {
	return c.postJSON(ctx, "/config", ConfigDoc{YAML: yaml})
}

// ValidateConfigDoc checks a structured config document without writing it.
func (c *Client) ValidateConfigDoc(ctx context.Context, doc json.RawMessage) error {
	return c.postJSON(ctx, "/config/validate", ConfigDoc{Doc: doc})
}

// SetConfigDoc validates + writes a structured config document and applies it — the
// primary path. The service re-serialises the structure to YAML, so the UI never
// handles YAML itself.
func (c *Client) SetConfigDoc(ctx context.Context, doc json.RawMessage) error {
	return c.postJSON(ctx, "/config", ConfigDoc{Doc: doc})
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("control: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError("GET", path, resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("control: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("control: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	// Side-effecting calls ack with 202 Accepted (mirroring the server).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return statusError("POST", path, resp)
	}
	return nil
}

// postJSONInto is postJSON that also decodes the answer. It exists because a POST
// that returns data is a real shape here: "refresh this subscription" is only
// useful if it says what came back.
func (c *Client) postJSONInto(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("control: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return statusError("POST", path, resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) postJSON(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("control: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return statusError("POST", path, resp)
	}
	return nil
}

func statusError(method, path string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := string(body)
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("control: %s %s: %s", method, path, msg)
}
