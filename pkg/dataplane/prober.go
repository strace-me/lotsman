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
func NewHTTPProber(targets map[string]string) *HTTPProber {
	return &HTTPProber{
		Client:  &http.Client{Timeout: 5 * time.Second},
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
