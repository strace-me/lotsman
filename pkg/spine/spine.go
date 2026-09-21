// Package spine holds the composition BOTH composition roots (cmd/lotsmand and
// client/core) must call, so a missing organ is a shared defect rather than an
// absent line in one front-end. It exists because the client, built months after
// the daemon, wired a SUBSET of the intelligence layer and nobody noticed: the
// two roots each tested what they had, both were green, and the only detector was
// a service misbehaving on the owner's device (LOT-65, LOT-74).
//
// The first and only tenant is Smarts: correlate/damper/adaptive/anomaly plus the
// tspu block-type classifier. The daemon wired it inline; the client did not wire
// it at all. Both now call Smarts, and a test asserts every hook is present.
package spine

import (
	"time"

	"github.com/strace-me/lotsman/pkg/adaptive"
	"github.com/strace-me/lotsman/pkg/anomaly"
	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/correlate"
	"github.com/strace-me/lotsman/pkg/damper"
	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/tspu"
)

// Smarts builds the intelligence layer wired into Brain's escalation decision,
// with the SAME tunables on every platform: the values here are the daemon's,
// moved verbatim out of cmd/lotsmand/main.go so they cannot drift per root.
//
// knowledge and catalog may be nil in tests; each hook degrades to the plain
// behaviour (the global threshold / no block-type preference) rather than
// panicking, because a daemon that will not start is worse than one without a
// preference.
func Smarts(knowledge *kb.KB, catalog *strategy.Catalog, cfg brain.Config, services []string) *brain.Smarts {
	corr := correlate.New(2, 0.6) // >=2 services and >=60% down => systemic
	damp := damper.New(10*time.Minute, 3, 30*time.Second, 10*time.Minute)
	tuner := adaptive.NewTuner(adaptive.Thresholds{EscalateAt: cfg.EscalateFails, RecoverAt: cfg.RecoverSuccess})
	anomalies := make(map[string]*anomaly.Detector, len(services))
	for _, name := range services {
		anomalies[name] = anomaly.New(anomaly.DefaultConfig())
	}

	return &brain.Smarts{
		FeedAnomaly: func(s string, ok bool, rtt int) {
			if d := anomalies[s]; d != nil {
				d.Observe(ok, rtt)
			}
		},
		ObserveHealth: func(s string, healthy bool) { corr.Set(s, healthy) },
		Threshold: func(s, activeStrategy string) int {
			if knowledge == nil {
				return cfg.EscalateFails
			}
			rel := knowledge.Stats(s, activeStrategy).Success
			return tuner.Tune(rel, damp.Count(s, time.Now())).EscalateAt
		},
		Systemic:    corr.Systemic,
		FlapBackoff: damp.Backoff,
		Anomaly: func(s string) anomaly.State {
			if d := anomalies[s]; d != nil {
				return d.State()
			}
			return anomaly.Healthy
		},
		RecordSwitch: damp.Record,
		SuggestClass: func(errText string, rttMs int) string {
			return tspu.SuggestClass(tspu.Classify(tspu.Signals{OK: false, Err: errText, RTTms: rttMs}))
		},
		// LOT-40: raw observed block-type + per-strategy declared block-types, so
		// zapret strategy resolution prefers one known to beat the current block.
		BlockType: func(errText string, rttMs int) string {
			return string(tspu.Classify(tspu.Signals{OK: false, Err: errText, RTTms: rttMs}))
		},
		BlockTypesFor: func(id string) []string {
			if catalog == nil {
				return nil
			}
			if d, ok := catalog.Resolve(id); ok {
				return d.BlockTypes
			}
			return nil
		},
	}
}
