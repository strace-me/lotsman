// Package burstprobe is the sustained-read (throughput) probe that feeds the
// throughput-aware fitness (pkg/tester). For each endpoint it pulls a chunk of
// real content past the ~16KB TSPU freeze cliff and times it, so a throttled path
// shows its true (collapsed) goodput rather than its pre-freeze burst — the signal
// loss/latency probes are blind to. It returns the WORST-case quality across the
// endpoints so a recipe is judged by its weakest domain (the multi-endpoint
// anti-Goodhart).
//
// It only READS. It never mutates the data plane — the caller wires it as a
// tester.Probe after applying an arm in the isolated test lane, and injects the
// http.Client (so the probe can be pinned to a SOCKS / SO_MARK dialer).
package burstprobe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/strace-me/lotsman/pkg/quality"
)

// DefaultTargetBytes is how far a single read pulls before scoring goodput — past
// the ~16KB freeze cliff so a throttled path reveals its collapsed rate.
const DefaultTargetBytes int64 = 64 << 10 // 64 KiB

// DefaultAttempts is reads per endpoint (>= tester MinSamples so the reading is
// trusted).
const DefaultAttempts = 5

// Probe measures sustained download quality across urls. Per URL it does
// `attempts` reads of up to `target` bytes, timing each, then builds a per-URL
// Quality (latency tail + goodput) and returns quality.Worst across the URLs. A
// read that delivers zero bytes is a failed attempt (loss); a read that delivers
// some bytes then stalls is recorded at its (low) goodput — both make a frozen
// path fail the throughput gate. target/attempts <= 0 use the defaults.
func Probe(ctx context.Context, client *http.Client, urls []string, target int64, attempts int) quality.Quality {
	eps := make([]Endpoint, 0, len(urls))
	for _, u := range urls {
		eps = append(eps, Endpoint{URL: u, Client: client})
	}
	return ProbeEndpoints(ctx, eps, target, attempts)
}

// Endpoint is one thing to pull and the client to pull it with. The client is
// per-endpoint because a service is not carried by one transport: YouTube's
// media is QUIC and its page is TCP, and a desync recipe has a separate profile
// for each. Measuring both with one client measures one profile and reports on
// two.
type Endpoint struct {
	URL    string
	Client *http.Client
}

// ProbeEndpoints is Probe with a transport per endpoint. The verdict is still
// quality.Worst across them, so a recipe whose TCP half carries and whose QUIC
// half does not is judged by the half that fails — which is the point: the user
// watching a video is on the failing half.
func ProbeEndpoints(ctx context.Context, eps []Endpoint, target int64, attempts int) quality.Quality {
	q, _ := ProbeEndpointsSaying(ctx, eps, target, attempts)
	return q
}

// ProbeEndpointsSaying is ProbeEndpoints that also hands back the transport's own
// first complaint.
//
// "Nothing was delivered" is where two completely different failures meet, and
// telling them apart decides whether a desync recipe is even the right kind of
// tool: `dial tcp …: i/o timeout` is a SYN that never got an answer, and nothing
// nfqws can do to a ClientHello matters, because there will be no ClientHello.
// `TLS handshake timeout` on a connection that established is the censor
// swallowing the hello, which is exactly what a recipe rewrites. The instrument
// held that sentence and threw it away, and both readings were then argued from
// a byte count of zero.
func ProbeEndpointsSaying(ctx context.Context, eps []Endpoint, target int64, attempts int) (quality.Quality, string) {
	if target <= 0 {
		target = DefaultTargetBytes
	}
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	qs := make([]quality.Quality, 0, len(eps))
	said := ""
	for _, ep := range eps {
		u, client := ep.URL, ep.Client
		var rtts []float64
		var bytesTotal int64
		var secsTotal float64
		// short: every attempt reached END OF BODY before the requested volume, so
		// this endpoint simply has less to give than we asked for. That is a fact
		// about the URL, not about the path, and the caller must be able to tell the
		// two apart — a stalled path does NOT reach end-of-body, it runs out of time.
		short := true
		for i := 0; i < attempts; i++ {
			n, dur, ended, err := readN(ctx, client, u, target)
			if err != nil && said == "" {
				said = err.Error()
			}
			if !ended || n >= target {
				short = false
			}
			if n <= 0 {
				continue // a failed attempt -> counts as loss (attempts is the denominator)
			}
			rtts = append(rtts, dur.Seconds()*1000)
			bytesTotal += n
			secsTotal += dur.Seconds()
		}
		var goodput float64
		if secsTotal > 0 {
			goodput = float64(bytesTotal) / 1024 / secsTotal // KiB/s
		}
		var avgBytes int64
		if got := len(rtts); got > 0 {
			avgBytes = bytesTotal / int64(got)
		}
		q := quality.FromBurst(rtts, attempts, avgBytes, goodput)
		if short {
			q = q.MarkShort()
		}
		qs = append(qs, q)
	}
	return quality.Worst(qs...), said
}

// readN GETs url and reads up to target bytes, returning the byte count and the
// wall time taken. A transport error or a body that stalls past the client/ctx
// deadline returns whatever was read before it stopped (n may be partial); n == 0
// means nothing was delivered.
// ended reports that the body finished on its own (EOF) rather than being cut
// short by the deadline — the difference between "this file is small" and "this
// path stalled", which a byte count alone cannot express.
func readN(ctx context.Context, client *http.Client, url string, target int64) (n int64, took time.Duration, ended bool, failed error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, false, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(start), false, err
	}
	defer resp.Body.Close()
	n, err = io.CopyN(io.Discard, resp.Body, target)
	if errors.Is(err, io.EOF) {
		return n, time.Since(start), true, nil
	}
	return n, time.Since(start), false, err
}
