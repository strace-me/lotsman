// Package balancer ranks VPN nodes/pools by connection quality with weights
// tuned per service category — finer than sing-box url-test, which picks purely
// by latency. Voice cares about jitter and loss; gaming about tail latency and
// jitter; streaming about bandwidth; general about raw success. The same
// quality.Quality (from active burst probes or passive ss tcp_info) feeds all
// of them; only the weights change.
package balancer

import (
	"sort"

	"github.com/strace-me/lotsman/pkg/quality"
)

// Weights expresses how much each metric matters (higher = matters more). They
// need not sum to anything; Score normalizes by their total. Reliability is the
// success/loss axis (quality tracks loss; success = 1 - loss).
type Weights struct {
	Reliability float64 // success rate (1 - loss)
	Latency     float64 // p95 tail latency
	Jitter      float64
	Bandwidth   float64
}

// ProfileFor returns the metric weights for a service category.
func ProfileFor(category string) Weights {
	switch category {
	case "voice", "messaging":
		return Weights{Reliability: 3, Latency: 1, Jitter: 3}
	case "gaming":
		return Weights{Reliability: 2, Latency: 3, Jitter: 3}
	case "streaming":
		return Weights{Reliability: 2, Latency: 0.5, Bandwidth: 3}
	default: // general
		return Weights{Reliability: 3, Latency: 1}
	}
}

// Candidate is a node/pool with its measured quality.
type Candidate struct {
	ID            string
	Q             quality.Quality
	BandwidthMbps float64 // 0 = unknown (needs a throughput probe)
}

// Score combines a candidate's metrics under the weights into a [0,1] score
// (higher is better). Each metric is mapped to a "goodness" in [0,1] first, so
// units (ms, Mbps) do not dominate; then a weighted average is taken over the
// weights that are non-zero AND have data.
func Score(c Candidate, w Weights) float64 {
	type term struct {
		good   float64
		weight float64
		known  bool
	}
	terms := []term{
		{1 - c.Q.Loss, w.Reliability, c.Q.Samples > 0},
		{inv(c.Q.P95ms, 100), w.Latency, c.Q.P95ms > 0},
		{inv(c.Q.JitterMs, 50), w.Jitter, c.Q.Samples > 1},
		{clamp01(c.BandwidthMbps / 100), w.Bandwidth, c.BandwidthMbps > 0},
	}
	var sum, wsum float64
	for _, t := range terms {
		if t.weight <= 0 || !t.known {
			continue
		}
		sum += t.good * t.weight
		wsum += t.weight
	}
	if wsum == 0 {
		return 0
	}
	return sum / wsum
}

// Rank sorts candidates best-first under the weights. Ties keep input order.
func Rank(cands []Candidate, w Weights) []Candidate {
	out := append([]Candidate(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool {
		return Score(out[i], w) > Score(out[j], w)
	})
	return out
}

// inv maps a "lower is better" metric to goodness in (0,1]: 0->1, scale->0.5.
func inv(v, scale float64) float64 {
	if v < 0 {
		v = 0
	}
	return scale / (scale + v)
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
