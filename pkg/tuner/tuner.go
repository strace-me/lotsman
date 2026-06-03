// Package tuner is the decision core of the "auto" knob mode: given an A/B
// measurement of a knob OFF (baseline) vs ON (variant), it decides whether the
// knob should be on — with a significance margin and hysteresis so it does not
// flip on noise. It is the brain only: deploying each arm and probing it (which
// needs the live data plane) is the caller's job; the verdict here is pure and
// unit-testable.
//
// This realizes the third knob mode from the spec: off (never) / on (always) /
// auto (the machine experiments and keeps what measurably helps — "не тупо").
package tuner

import (
	"strings"
	"sync"

	"github.com/strace-me/lotsman/pkg/quality"
)

// Mode is a knob's control mode.
type Mode string

const (
	ModeOff  Mode = "off"  // never emit the knob
	ModeOn   Mode = "on"   // always emit it
	ModeAuto Mode = "auto" // let the tuner decide by measurement
)

// ParseMode reads a config value. "" / "off" / "false" / "no" -> off;
// "on" / "true" / "yes" -> on; "auto" -> auto. Unknown -> off (safe default).
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		return ModeOn
	case "auto":
		return ModeAuto
	default:
		return ModeOff
	}
}

// Tuner remembers each auto-knob's current on/off state so it can apply
// hysteresis across cycles. Safe for concurrent use.
type Tuner struct {
	// margin is the relative improvement (e.g. 0.1 = 10%) the challenger must show
	// to flip the current state — the hysteresis band that prevents flapping.
	margin float64

	mu  sync.Mutex
	on  map[string]bool
	set map[string]bool // whether we have a remembered state for the key
}

// New builds a Tuner. margin<=0 falls back to 0.1 (10%).
func New(margin float64) *Tuner {
	if margin <= 0 {
		margin = 0.1
	}
	return &Tuner{margin: margin, on: map[string]bool{}, set: map[string]bool{}}
}

// Resolve decides whether knob `key` should be ON given fresh measurements of
// the knob OFF (baseline) and ON (variant). It returns the chosen state and
// whether it changed from the remembered one. Hysteresis: to TURN ON, variant
// must be clearly better than baseline; to TURN OFF, variant must be clearly
// worse; otherwise the current state holds. First sight with no history adopts
// whichever arm is clearly better, else OFF.
func (t *Tuner) Resolve(key string, baseline, variant quality.Quality) (on, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cur, have := t.on[key], t.set[key]
	variantBetter := better(variant, baseline, t.margin)
	baselineBetter := better(baseline, variant, t.margin)

	var next bool
	switch {
	case !have:
		next = variantBetter // no history: turn on only if the variant clearly wins
	case cur:
		next = !baselineBetter // currently on: stay on unless baseline clearly wins
	default:
		next = variantBetter // currently off: turn on only if variant clearly wins
	}

	changed = !have || next != cur
	t.on[key] = next
	t.set[key] = true
	return next, changed
}

// State returns the remembered on/off for a key (false if unseen).
func (t *Tuner) State(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.on[key]
}

// better reports whether a is meaningfully better than b under margin.
// Connectivity dominates: clearly fewer losses wins outright; on comparable loss,
// a wins only if its tail latency (p95) beats b's by at least the margin.
func better(a, b quality.Quality, margin float64) bool {
	// Loss first: a clear loss advantage (more than the margin) wins.
	if a.Loss+margin < b.Loss {
		return true
	}
	if b.Loss+margin < a.Loss {
		return false // a is clearly lossier -> not better
	}
	// Comparable loss: compare tail latency with the margin band.
	if a.P95ms <= 0 && b.P95ms <= 0 {
		return false // no latency signal either way -> not "clearly better"
	}
	if b.P95ms <= 0 {
		return false
	}
	return a.P95ms < b.P95ms*(1-margin)
}
