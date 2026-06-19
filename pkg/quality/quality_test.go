package quality

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.5 }

func TestPercentilesAndLoss(t *testing.T) {
	// 10 successful samples 10..100ms, plus 2 failed attempts (12 total).
	ok := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	q := FromRTTs(ok, 12)

	if !approx(q.P50ms, 55) { // median of 10..100
		t.Errorf("p50 = %.1f, want ~55", q.P50ms)
	}
	if q.P95ms <= q.P50ms || q.P99ms < q.P95ms {
		t.Errorf("tail ordering wrong: p50=%.1f p95=%.1f p99=%.1f", q.P50ms, q.P95ms, q.P99ms)
	}
	if !approx(q.P99ms, 99.1) && q.P99ms < 95 {
		t.Errorf("p99 = %.1f, want near 100", q.P99ms)
	}
	if !approx(q.Loss, 2.0/12.0) {
		t.Errorf("loss = %.3f, want %.3f", q.Loss, 2.0/12.0)
	}
}

func TestTailCatchesSpikes(t *testing.T) {
	// Low mean but occasional spikes: p99 must expose them.
	samples := make([]float64, 0, 100)
	for i := 0; i < 95; i++ {
		samples = append(samples, 20) // steady 20ms
	}
	for i := 0; i < 5; i++ {
		samples = append(samples, 500) // 5% spikes to 500ms
	}
	q := FromRTTs(samples, 100)
	if q.P50ms != 20 {
		t.Errorf("p50 = %.1f, want 20 (mean would lie)", q.P50ms)
	}
	if q.P99ms < 400 {
		t.Errorf("p99 = %.1f, want to expose the 500ms spikes", q.P99ms)
	}
}

func TestJitter(t *testing.T) {
	steady := FromRTTs([]float64{50, 50, 50, 50}, 4)
	if steady.JitterMs != 0 {
		t.Errorf("steady jitter = %.1f, want 0", steady.JitterMs)
	}
	bumpy := FromRTTs([]float64{20, 80, 20, 80}, 4) // diffs 60,60,60
	if !approx(bumpy.JitterMs, 60) {
		t.Errorf("bumpy jitter = %.1f, want ~60", bumpy.JitterMs)
	}
}

func TestAllFailed(t *testing.T) {
	q := FromRTTs(nil, 5)
	if q.Loss != 1.0 || q.P99ms != 0 {
		t.Errorf("all-failed: loss=%.2f p99=%.1f, want 1.0/0", q.Loss, q.P99ms)
	}
}

func TestFromBurstCarriesThroughput(t *testing.T) {
	q := FromBurst([]float64{40, 50, 60}, 3, 96*1024, 250.0)
	if q.Bytes != 96*1024 || q.GoodputKBps != 250.0 {
		t.Errorf("throughput not carried: bytes=%d goodput=%v", q.Bytes, q.GoodputKBps)
	}
	if q.Loss != 0 || q.P95ms == 0 {
		t.Errorf("latency dimension lost: loss=%v p95=%v", q.Loss, q.P95ms)
	}
}

func TestWorstTakesWeakestEndpoint(t *testing.T) {
	a := Quality{Loss: 0.0, GoodputKBps: 500, Bytes: 100000, P95ms: 30, Samples: 10}
	b := Quality{Loss: 0.4, GoodputKBps: 2, Bytes: 2048, P95ms: 200, Samples: 6} // a frozen/broken endpoint
	w := Worst(a, b)
	if w.Loss != 0.4 || w.GoodputKBps != 2 || w.Bytes != 2048 || w.P95ms != 200 || w.Samples != 6 {
		t.Errorf("worst-case aggregate wrong: %+v", w)
	}
	if got := Worst(); got != (Quality{}) {
		t.Errorf("empty Worst should be zero, got %+v", got)
	}
}
