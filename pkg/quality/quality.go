// Package quality computes connection-quality metrics from latency samples:
// the percentile tail (p50/p95/p99), jitter, and loss. For real-time traffic
// (voice, gaming) the mean latency lies — a path with a low mean but a high p99
// stutters — so selection should weigh the tail and jitter, not the average.
//
// The same Quality type is produced two ways: actively from a burst probe's
// samples (FromRTTs), and passively from the kernel's tcp_info on real flows
// (see dataplane ss parsing). One vocabulary for both sources.
package quality

import "sort"

// Quality is the assessed connection quality over a set of samples.
type Quality struct {
	P50ms    float64 // median latency
	P95ms    float64 // tail latency (the "L95")
	P99ms    float64 // worse tail (the "L99")
	JitterMs float64 // mean inter-sample RTT variation (RFC3550-style)
	Loss     float64 // failed / total, in [0,1]
	Samples  int     // total attempts considered

	// Throughput dimension — set only by a sustained-read (burst-download) probe.
	// A latency-only probe leaves these zero. They exist to catch the TSPU
	// IP-volume-freeze, where a path connects with LOW loss but GOODPUT collapses
	// after ~16KB — invisible to loss/latency alone.
	GoodputKBps float64 // sustained download rate over the read, KiB/s
	Bytes       int64   // total bytes pulled (0 = froze from the first byte)
}

// FromRTTs builds Quality from the round-trip times of successful samples (in
// order) plus the total number of attempts (to derive loss). okRTTs must be in
// sample order so jitter reflects consecutive variation.
func FromRTTs(okRTTs []float64, attempts int) Quality {
	q := Quality{Samples: attempts}
	if attempts > 0 {
		q.Loss = float64(attempts-len(okRTTs)) / float64(attempts)
	}
	if len(okRTTs) == 0 {
		return q
	}

	q.JitterMs = jitter(okRTTs)

	sorted := append([]float64(nil), okRTTs...)
	sort.Float64s(sorted)
	q.P50ms = percentile(sorted, 0.50)
	q.P95ms = percentile(sorted, 0.95)
	q.P99ms = percentile(sorted, 0.99)
	return q
}

// FromBurst builds Quality from a sustained-read probe: per-request latencies +
// attempts (loss/tail, as FromRTTs) plus the total bytes pulled and the achieved
// goodput. Use this for the throughput-aware fitness; goodputKBps is what the
// freeze gate keys on.
func FromBurst(okRTTs []float64, attempts int, bytes int64, goodputKBps float64) Quality {
	q := FromRTTs(okRTTs, attempts)
	q.Bytes = bytes
	q.GoodputKBps = goodputKBps
	return q
}

// Worst combines per-endpoint qualities into a single worst-case aggregate: max
// loss, MIN goodput/bytes, max latency tail, min samples. A multi-endpoint probe
// uses it so a recipe that fixes one endpoint but breaks another is judged by the
// weakest endpoint — defeating the single-canary Goodhart. Empty input is a zero
// Quality (no samples → untrusted).
func Worst(qs ...Quality) Quality {
	if len(qs) == 0 {
		return Quality{}
	}
	w := qs[0]
	for _, q := range qs[1:] {
		if q.Loss > w.Loss {
			w.Loss = q.Loss
		}
		if q.GoodputKBps < w.GoodputKBps {
			w.GoodputKBps = q.GoodputKBps
		}
		if q.Bytes < w.Bytes {
			w.Bytes = q.Bytes
		}
		if q.P50ms > w.P50ms {
			w.P50ms = q.P50ms
		}
		if q.P95ms > w.P95ms {
			w.P95ms = q.P95ms
		}
		if q.P99ms > w.P99ms {
			w.P99ms = q.P99ms
		}
		if q.JitterMs > w.JitterMs {
			w.JitterMs = q.JitterMs
		}
		if q.Samples < w.Samples {
			w.Samples = q.Samples
		}
	}
	return w
}

// percentile uses linear interpolation between closest ranks (type 7), the
// common default. sorted must be ascending and non-empty.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 1 {
		return sorted[0]
	}
	rank := p * float64(n-1)
	lo := int(rank)
	frac := rank - float64(lo)
	if lo+1 >= n {
		return sorted[n-1]
	}
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

// jitter is the mean absolute difference between consecutive samples (RFC3550
// interarrival jitter, simplified): captures how "bumpy" the path is.
func jitter(rtts []float64) float64 {
	if len(rtts) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(rtts); i++ {
		d := rtts[i] - rtts[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / float64(len(rtts)-1)
}
