// Package domainscan answers "from the router, which domains of a rule actually
// work and which are blocked?" — by probing each domain over the box's own path
// (which traverses the live nfqws desync, so a failure means "blocked despite
// whatever zapret is doing now"). It is the discovery half of per-domain tuning:
// the blocked set is what the strategy tester then tries to fix, domain by domain.
//
// OK means the request reached the server and got ANY HTTP response (even 4xx) —
// connectivity, not content (mirrors the rest of Lotsman's probe semantics). A
// transport error / timeout / reset is the DPI-block signature.
package domainscan

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// Verdict interprets a probe Result: only a transport timeout/reset is the DPI
// block signature. A TLS cert mismatch or a DNS no-such-host means the apex just
// isn't a real host (common for CDN/suffix domains like *.ytimg.com), and a QUIC
// CRYPTO_ERROR means the handshake REACHED the server — none of those are
// censorship, so they must not be reported as "blocked".
type Verdict string

const (
	VerdictOK           Verdict = "ok"
	VerdictBlocked      Verdict = "blocked" // timeout / reset / refused — needs treatment
	VerdictInconclusive Verdict = "n/a"     // cert / dns / crypto-error — apex artifact, not censorship
)

// Classify maps a Result to a Verdict. Pure.
func Classify(r Result) Verdict {
	if r.OK {
		return VerdictOK
	}
	e := strings.ToLower(r.Err)
	switch {
	case strings.Contains(e, "timeout"),
		strings.Contains(e, "deadline exceeded"),
		strings.Contains(e, "connection reset"),
		strings.Contains(e, "connection refused"),
		strings.Contains(e, "no recent network activity"): // quic idle timeout
		return VerdictBlocked
	default:
		// tls verify failure, "no such host"/"lookup", quic CRYPTO_ERROR, etc.
		return VerdictInconclusive
	}
}

// Result is one domain's probe outcome.
type Result struct {
	Domain string
	OK     bool
	Status int    // HTTP status when reached (0 if not)
	RTTms  int    // round-trip of the probe
	Err    string // transport error when !OK
}

// ProbeFunc probes a single domain. Injected so ProbeAll is testable without
// network; HTTPProbe returns the real implementation.
type ProbeFunc func(ctx context.Context, domain string) Result

// HTTPProbe returns a ProbeFunc that GETs https://<domain>/ with the given
// timeout. It does not follow into content: any HTTP response = OK (reachable).
func HTTPProbe(timeout time.Duration) ProbeFunc {
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context, domain string) Result {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+domain+"/", nil)
		if err != nil {
			return Result{Domain: domain, Err: err.Error()}
		}
		start := time.Now()
		resp, err := client.Do(req)
		rtt := int(time.Since(start).Milliseconds())
		if err != nil {
			return Result{Domain: domain, RTTms: rtt, Err: err.Error()}
		}
		resp.Body.Close()
		return Result{Domain: domain, OK: true, Status: resp.StatusCode, RTTms: rtt}
	}
}

// HTTP3Probe returns a ProbeFunc that GETs https://<domain>/ over HTTP/3 (QUIC,
// UDP/443) with a real QUIC+TLS handshake — the faithful test, the same exchange
// a browser does, so a hand-rolled probe can't give a false verdict. A timeout
// means QUIC is blocked even when TCP/443 works (the discord-style "TCP ok, UDP
// dead" trap). Any HTTP/3 response = OK. A fresh transport per probe avoids
// connection reuse masking a block.
func HTTP3Probe(timeout time.Duration) ProbeFunc {
	return func(ctx context.Context, domain string) Result {
		tr := &http3.Transport{}
		defer tr.Close()
		client := &http.Client{Transport: tr, Timeout: timeout}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+domain+"/", nil)
		if err != nil {
			return Result{Domain: domain, Err: err.Error()}
		}
		start := time.Now()
		resp, err := client.Do(req)
		rtt := int(time.Since(start).Milliseconds())
		if err != nil {
			return Result{Domain: domain, RTTms: rtt, Err: err.Error()}
		}
		resp.Body.Close()
		return Result{Domain: domain, OK: true, Status: resp.StatusCode, RTTms: rtt}
	}
}

// ProbeAll probes every domain concurrently (bounded by concurrency) and returns
// results in the input order. A nil/zero concurrency defaults to 8.
func ProbeAll(ctx context.Context, domains []string, probe ProbeFunc, concurrency int) []Result {
	if concurrency < 1 {
		concurrency = 8
	}
	out := make([]Result, len(domains))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = probe(ctx, d)
		}(i, d)
	}
	wg.Wait()
	return out
}

// Blocked returns the subset of results whose Verdict is Blocked (the actionable,
// censorship-signature set — excludes cert/DNS/crypto artifacts), sorted by
// domain. Pure.
func Blocked(results []Result) []Result {
	var b []Result
	for _, r := range results {
		if Classify(r) == VerdictBlocked {
			b = append(b, r)
		}
	}
	sort.Slice(b, func(i, j int) bool { return b[i].Domain < b[j].Domain })
	return b
}

// Tally counts results by verdict. Pure.
func Tally(results []Result) (ok, blocked, inconclusive int) {
	for _, r := range results {
		switch Classify(r) {
		case VerdictOK:
			ok++
		case VerdictBlocked:
			blocked++
		default:
			inconclusive++
		}
	}
	return
}
