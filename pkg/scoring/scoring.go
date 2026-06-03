// Package scoring turns connection metrics (kb.Stats) into a single comparable
// number, weighted per service category. Latency alone is misleading: a voice
// call cares far more about jitter and loss than raw RTT, while streaming cares
// about success/throughput and tolerates latency. A profile encodes those
// weights so the selector ranks strategies/nodes by what actually matters for
// the service in question.
package scoring

import "github.com/strace-me/lotsman/pkg/kb"

// Profile weights each metric's contribution. Success is rewarded; latency,
// jitter and loss are penalized. Weights need not sum to anything particular.
type Profile struct {
	Success float64
	Latency float64
	Jitter  float64
	Loss    float64
}

// Built-in profiles per category.
var (
	// General: correctness first, mild latency awareness.
	General = Profile{Success: 1.0, Latency: 0.2, Jitter: 0.1, Loss: 0.3}
	// Voice/gaming (real-time UDP): jitter and loss dominate — they cause the
	// stutter, not average latency.
	Voice = Profile{Success: 0.8, Latency: 0.3, Jitter: 0.9, Loss: 0.9}
	// Streaming: success/throughput matter, latency is cheap (buffered).
	Streaming = Profile{Success: 1.0, Latency: 0.1, Jitter: 0.05, Loss: 0.4}
)

// ProfileFor maps a service category to a profile.
func ProfileFor(category string) Profile {
	switch category {
	case "voice", "gaming", "games":
		return Voice
	case "streaming":
		return Streaming
	default:
		return General
	}
}

// Score combines stats under a profile; higher is better. Latency and jitter
// are normalized to [0,1] (≈500ms / ≈200ms = full penalty) so no single metric
// can dominate unboundedly.
func Score(s kb.Stats, p Profile) float64 {
	return p.Success*s.Success -
		p.Latency*norm(s.RTTms, 500) -
		p.Jitter*norm(s.JitterMs, 200) -
		p.Loss*s.Loss
}

// ScorerFor returns a score function over strategy IDs for one service and
// category, ready to hand to selector.Select.
func ScorerFor(k *kb.KB, service, category string) func(strategyID string) float64 {
	p := ProfileFor(category)
	return func(strategyID string) float64 {
		return Score(k.Stats(service, strategyID), p)
	}
}

func norm(v, full float64) float64 {
	if v <= 0 {
		return 0
	}
	r := v / full
	if r > 1 {
		return 1
	}
	return r
}
