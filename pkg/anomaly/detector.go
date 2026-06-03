// Package anomaly turns a stream of probe results into a richer health signal
// than a simple up/down: it distinguishes "degraded" (intermittent loss or a
// latency spike) from "down" (mostly failing). Degraded lets Lotsman react
// before a total outage — the gap seen with Discord voice, which lagged and
// blipped rather than dying outright.
package anomaly

// State is the assessed health of a service over a recent window.
type State string

const (
	Healthy  State = "healthy"
	Degraded State = "degraded"
	Down     State = "down"
)

// Config tunes the detector. Defaults are sensible for second-scale probes.
type Config struct {
	Window      int     // number of recent samples to assess
	MinSamples  int     // need at least this many before judging (else Healthy)
	DownLoss    float64 // loss fraction >= this => Down
	DegradeLoss float64 // loss fraction >= this (and < DownLoss) => Degraded
	RTTFactor   float64 // recent ok-RTT > baseline*factor => Degraded
	RTTAlpha    float64 // EWMA weight for the RTT baseline
}

// DefaultConfig returns reasonable thresholds.
func DefaultConfig() Config {
	return Config{
		Window: 10, MinSamples: 5,
		DownLoss: 0.6, DegradeLoss: 0.2,
		RTTFactor: 2.5, RTTAlpha: 0.2,
	}
}

type sample struct {
	ok    bool
	rttMs int
}

// Detector tracks one service's recent probe results.
type Detector struct {
	cfg      Config
	ring     []sample
	pos      int
	count    int
	baseline float64 // EWMA of successful RTTs; 0 = unset
}

// New builds a detector.
func New(cfg Config) *Detector {
	if cfg.Window <= 0 {
		cfg = DefaultConfig()
	}
	return &Detector{cfg: cfg, ring: make([]sample, cfg.Window)}
}

// Observe records a probe result.
func (d *Detector) Observe(ok bool, rttMs int) {
	d.ring[d.pos] = sample{ok: ok, rttMs: rttMs}
	d.pos = (d.pos + 1) % d.cfg.Window
	if d.count < d.cfg.Window {
		d.count++
	}
	if ok && rttMs > 0 {
		switch {
		case d.baseline == 0:
			d.baseline = float64(rttMs)
		case float64(rttMs) <= d.baseline*d.cfg.RTTFactor:
			// Only learn from non-spike samples, so a sustained spike does not
			// creep into the baseline and mask itself.
			d.baseline = d.cfg.RTTAlpha*float64(rttMs) + (1-d.cfg.RTTAlpha)*d.baseline
		}
	}
}

// State assesses current health over the window.
func (d *Detector) State() State {
	if d.count < d.cfg.MinSamples {
		return Healthy // not enough data to cry wolf
	}
	var failed, okN, rttSum int
	for i := 0; i < d.count; i++ {
		s := d.ring[i]
		if s.ok {
			okN++
			rttSum += s.rttMs
		} else {
			failed++
		}
	}
	loss := float64(failed) / float64(d.count)
	switch {
	case loss >= d.cfg.DownLoss:
		return Down
	case loss >= d.cfg.DegradeLoss:
		return Degraded
	}
	// Latency-spike degradation: recent successful RTT well above baseline.
	if okN > 0 && d.baseline > 0 {
		avg := float64(rttSum) / float64(okN)
		if avg > d.baseline*d.cfg.RTTFactor {
			return Degraded
		}
	}
	return Healthy
}

// BaselineRTT exposes the current RTT baseline (for metrics).
func (d *Detector) BaselineRTT() float64 { return d.baseline }
