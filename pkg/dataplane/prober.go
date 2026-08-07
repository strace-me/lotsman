// Package dataplane holds the primitives that touch the network: probes, the
// Clash API client, and nft set manipulation. It is a leaf shared by the
// production Applier (and, later, the sandbox Tester). It contains no policy.
package dataplane

import (
	"context"
	"net/http"
	"time"

	"github.com/strace-me/lotsman/pkg/events"
)

// Prober runs one active probe for a service and returns a verdict. Position
// is carried through so the caller (Brain) knows whether this was the active
// strategy or a silent recovery probe.
type Prober interface {
	Probe(ctx context.Context, service string, position int) events.ProductionVerdict
}

// HTTPProber issues a GET and treats a response (any status) as reachable.
// M0 uses a single probe type; per-category probe templates come later.
type HTTPProber struct {
	Client  *http.Client
	Targets map[string]string // service -> URL
}

// NewHTTPProber builds a prober with a short per-probe timeout.
//
// DisableKeepAlives is load-bearing, and leaving it off was measured as a probe
// that could not see the censorship it exists to detect. Establishing a
// connection is the thing TSPU blocks — it lets the TCP handshake complete and
// then swallows the TLS ClientHello — so a probe riding a connection opened
// minutes ago never presents the censor with anything to block. On the ThinkPad,
// with a 10s probe interval against Go's 90s idle timeout, one successful
// handshake to youtube kept the pooled connection alive indefinitely: the probe
// answered 204 in ~57ms with `fails=0` and `stalledRatio=0.04` while the owner's
// curl, opening a fresh connection each time, timed out three times out of three
// and the browser would not load the site at all.
//
// NewMultiProberProxy already carried this for the SOCKS path, with its own
// incident behind it (a pooled connection kept the outbound it was opened with,
// so a dead VPN node reported healthy forever after the brain had escalated
// away). The requirement was written down there and implemented on exactly one
// of the two constructors — the direct path, which is what a tun-mode client and
// the router daemon both use, kept Go's shared pooling transport.
func NewHTTPProber(targets map[string]string) *HTTPProber {
	return &HTTPProber{
		Client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
		Targets: targets,
	}
}

func (p *HTTPProber) Probe(ctx context.Context, service string, position int) events.ProductionVerdict {
	v := events.ProductionVerdict{Service: service, Position: position}
	url, ok := p.Targets[service]
	if !ok {
		v.Err = "no probe target for service"
		return v
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	resp, err := p.Client.Do(req)
	v.RTTms = int(time.Since(start).Milliseconds())
	if err != nil {
		v.Err = err.Error()
		return v
	}
	resp.Body.Close()
	v.OK = true
	return v
}
