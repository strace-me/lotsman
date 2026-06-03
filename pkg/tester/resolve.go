package tester

import (
	"context"
	"time"

	"github.com/strace-me/lotsman/pkg/quality"
)

// Arm is one data-plane configuration to A/B-test for a service: Apply puts the
// data plane into that state (the baseline arm = "clean", no desync; each recipe
// arm applies that recipe scoped to the service). ID labels it for the verdict.
type Arm struct {
	ID    string
	Apply func(context.Context) error
}

// Probe measures the service's path quality in the data plane's CURRENT state —
// e.g. an HTTP burst for TCP services, or stunprobe.Probe for voice (UDP). It is
// called after an arm is applied and settled.
type Probe func(context.Context) quality.Quality

// Resolve runs the measured A/B that picks a service's zapret handling without
// guessing. For the baseline arm and each recipe arm it: applies the arm, waits
// `settle` (so the path stabilises and any RTC re-negotiates — the thing the
// premature-rollback bug missed), then probes. It then asks Decide for the
// verdict. All data-plane mutations and the probe are injected, so this stays
// pure and unit-testable; the caller wires the live nfqws toggle + prober and,
// on the returned verdict, applies the winner (or leaves it for Brain to escalate
// when the rung is Unviable).
//
// The trials are returned alongside the verdict for logging/telemetry. An arm
// whose Apply errors is skipped (it simply contributes no trial); a baseline
// whose Apply errors is fatal (we cannot judge without it).
func Resolve(ctx context.Context, baseline Arm, recipes []Arm, probe Probe, settle time.Duration, cfg Config) (Verdict, []Trial, error) {
	measure := func(a Arm) (quality.Quality, error) {
		if err := a.Apply(ctx); err != nil {
			return quality.Quality{}, err
		}
		if settle > 0 {
			select {
			case <-time.After(settle):
			case <-ctx.Done():
				return quality.Quality{}, ctx.Err()
			}
		}
		return probe(ctx), nil
	}

	baseQ, err := measure(baseline)
	if err != nil {
		return Verdict{}, nil, err
	}

	trials := make([]Trial, 0, len(recipes))
	for _, r := range recipes {
		q, err := measure(r)
		if err != nil {
			continue // a failed arm just doesn't contribute a trial
		}
		trials = append(trials, Trial{RecipeID: r.ID, Q: q})
	}
	return Decide(baseQ, trials, cfg), trials, nil
}
