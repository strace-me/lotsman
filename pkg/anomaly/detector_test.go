package anomaly

import "testing"

func feed(d *Detector, n int, ok bool, rtt int) {
	for i := 0; i < n; i++ {
		d.Observe(ok, rtt)
	}
}

func TestHealthyWhenAllOK(t *testing.T) {
	d := New(DefaultConfig())
	feed(d, 10, true, 20)
	if got := d.State(); got != Healthy {
		t.Errorf("state = %s, want healthy", got)
	}
}

func TestInsufficientSamplesIsHealthy(t *testing.T) {
	d := New(DefaultConfig())
	feed(d, 3, false, 0) // below MinSamples=5
	if got := d.State(); got != Healthy {
		t.Errorf("state = %s, want healthy (not enough data)", got)
	}
}

func TestDownWhenMostlyFailing(t *testing.T) {
	d := New(DefaultConfig())
	feed(d, 2, true, 20)
	feed(d, 8, false, 0) // 80% loss >= DownLoss 0.6
	if got := d.State(); got != Down {
		t.Errorf("state = %s, want down", got)
	}
}

func TestDegradedOnIntermittentLoss(t *testing.T) {
	d := New(DefaultConfig())
	feed(d, 7, true, 20)
	feed(d, 3, false, 0) // 30% loss: in [0.2, 0.6) => degraded
	if got := d.State(); got != Degraded {
		t.Errorf("state = %s, want degraded", got)
	}
}

func TestDegradedOnLatencySpike(t *testing.T) {
	d := New(DefaultConfig())
	feed(d, 10, true, 20) // baseline ~20ms
	// Sustained spikes (no loss) should read as degraded, not healthy.
	feed(d, 5, true, 200)
	if got := d.State(); got != Degraded {
		t.Errorf("state = %s, want degraded on latency spike (baseline=%.0f)", got, d.BaselineRTT())
	}
}

func TestRecoversToHealthy(t *testing.T) {
	d := New(DefaultConfig())
	feed(d, 5, false, 0) // down
	if d.State() == Healthy {
		t.Fatal("setup: should be unhealthy")
	}
	feed(d, 10, true, 25) // window refills with good samples
	if got := d.State(); got != Healthy {
		t.Errorf("state = %s, want healthy after recovery", got)
	}
}

// LOT-38: a fluke-fast first sample must not latch the baseline low and report a
// steady (higher) normal RTT as Degraded forever. Warm-up learns unconditionally.
func TestBaselineDoesNotLatchOnFastFirstSample(t *testing.T) {
	d := New(DefaultConfig())
	d.Observe(true, 8)    // fluke-fast first sample seeds baseline=8
	feed(d, 12, true, 40) // true steady-state ~40ms (> 8*2.5=20: would latch pre-fix)
	if got := d.State(); got != Healthy {
		t.Errorf("steady 40ms after a fluke-fast seed must read Healthy, got %s (baseline=%.0f)", got, d.BaselineRTT())
	}
}
