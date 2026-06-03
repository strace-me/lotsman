// Package adaptive tunes the escalate/recover thresholds per service from its
// observed behavior, instead of one fixed pair for everyone. A chronically
// flaky service (low success rate) should tolerate more failures before
// switching, so Lotsman does not churn on its normal noise; a service that has
// been flapping should have to prove stability longer before a recovery is
// trusted. A rock-solid service keeps the tight base thresholds, so a sudden
// failure is acted on quickly.
package adaptive

// Thresholds is an escalate/recover pair.
type Thresholds struct {
	EscalateAt int
	RecoverAt  int
}

// Tuner derives per-service thresholds from a base.
type Tuner struct {
	base           Thresholds
	escalateSpread int // max extra escalate failures for a fully-unreliable service
	recoverSpread  int // max extra recover successes from flapping
}

// NewTuner builds a tuner over base thresholds with default spreads.
func NewTuner(base Thresholds) *Tuner {
	return &Tuner{base: base, escalateSpread: 4, recoverSpread: 6}
}

// Tune returns adjusted thresholds. reliability is the service's success rate in
// [0,1] (e.g. the KB EWMA); flapCount is how many recent transitions it has had.
func (t *Tuner) Tune(reliability float64, flapCount int) Thresholds {
	if reliability < 0 {
		reliability = 0
	}
	if reliability > 1 {
		reliability = 1
	}
	// Less reliable -> escalate slower (tolerate its normal flakiness).
	escalate := t.base.EscalateAt + round((1-reliability)*float64(t.escalateSpread))

	// More flapping (and lower reliability) -> demand more proof before recover.
	extra := flapCount + round((1-reliability)*2)
	if extra > t.recoverSpread {
		extra = t.recoverSpread
	}
	recover := t.base.RecoverAt + extra

	return Thresholds{EscalateAt: escalate, RecoverAt: recover}
}

func round(f float64) int { return int(f + 0.5) }
