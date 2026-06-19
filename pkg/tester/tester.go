// Package tester is the decision core of the zapret strategy tester. It is an
// ADVISOR to the zapret rung of a service's chain — it does NOT own the chain.
//
// The division of labour (the single-owner rule we hold everywhere):
//   - Brain owns the LADDER: which rung a service sits on (zapret -> VPN ->
//     emergency) and the escalation between rungs. Brain is the only decider of
//     the rung, and the applier is the only writer of the data plane.
//   - noderank advises the VPN rung: "best NODE".
//   - tester (this package) advises the zapret rung: "best RECIPE, or this rung
//     can't be made to work".
//
// So tester and noderank are twins — one picks a node for the VPN step, the other
// a recipe for the zapret step — and NEITHER touches the ladder. In particular
// this engine never says "go to VPN": it reports OutcomeUnviable and Brain, which
// already escalates a rung that fails its probes, decides the consequence.
//
// It holds no I/O. The caller measures each arm — direct-without-desync (the
// baseline) and each candidate recipe (an isolated burst probe through the scoped
// test lane) — and hands the resulting quality.Quality values here. The engine
// kills the false positives of "does the video open?" testers by demanding a
// baseline A/B (a recipe must measurably BEAT direct, which also recognises "the
// site was never blocked"), enough samples to trust a reading, and that the
// recipe be reliable in its own right.
package tester

import "github.com/strace-me/lotsman/pkg/quality"

// Outcome is the engine's recommendation FOR THE ZAPRET RUNG (never a rung
// change — that is Brain's).
type Outcome string

const (
	// OutcomeNoDesync: direct-without-desync already works — the service is not
	// blocked, so the zapret rung carries it with NO recipe. The safe default
	// (desync can break working sites). Viable.
	OutcomeNoDesync Outcome = "no_desync"
	// OutcomeRecipe: a recipe reliably beats the baseline — the zapret rung
	// carries the service with that recipe as its --new block. Viable.
	OutcomeRecipe Outcome = "recipe"
	// OutcomeUnviable: the service is blocked and no recipe beats the baseline —
	// the zapret rung cannot hold it. This is a FACT reported to Brain, not a
	// rung decision: Brain escalates (to VPN) on its own.
	OutcomeUnviable Outcome = "unviable"
)

// Trial is one candidate recipe and the quality measured while it was applied
// (scoped to the service's domains, over a burst of real-content probes).
type Trial struct {
	RecipeID string
	Q        quality.Quality
}

// Verdict is the engine's advice for the zapret rung.
type Verdict struct {
	Outcome  Outcome
	RecipeID string // set when Outcome == OutcomeRecipe
	Reason   string
}

// Viable reports whether the zapret rung can carry the service (with or without
// a recipe). When false, Brain should escalate to the next rung.
func (v Verdict) Viable() bool { return v.Outcome != OutcomeUnviable }

// Config holds the decision thresholds.
type Config struct {
	BaselineOKLoss float64 // baseline loss at/below this => not blocked => NoDesync (e.g. 0.10)
	MaxRecipeLoss  float64 // a chosen recipe's own loss must be at/below this (e.g. 0.10)
	LossMargin     float64 // a recipe must beat baseline loss by at least this to count as helping (e.g. 0.20)
	MinSamples     int     // a quality from fewer attempts is untrusted (e.g. 5)

	// Throughput gate — engaged only when MinGoodputKBps > 0 (i.e. the probe
	// measured goodput). When off, the verdict is loss-only (the original
	// behaviour, for latency probes / voice). When on, a "healthy" baseline and a
	// winning recipe must BOTH sustain >= MinGoodputKBps, and a recipe is also
	// credited if it lifts goodput by >= GoodputMargin (catches the IP-freeze,
	// where loss is low but goodput collapses — loss alone can't see it).
	MinGoodputKBps float64
	GoodputMargin  float64
}

// DefaultConfig returns sane LOSS-only thresholds: not blocked if direct loses
// <=10%; a recipe is chosen only if it loses <=10% itself AND cuts loss by >=20
// points vs baseline; readings need >=5 samples. Throughput gate OFF.
func DefaultConfig() Config {
	return Config{BaselineOKLoss: 0.10, MaxRecipeLoss: 0.10, LossMargin: 0.20, MinSamples: 5}
}

// ThroughputConfig is DefaultConfig plus the throughput gate: a path counts as
// healthy/working only if it sustains >=64 KiB/s (~512 kbit/s — well above the
// ~1 KiB/s freeze floor, below any real path), and a recipe is credited for a
// goodput gain of >=32 KiB/s. Use for sustained-read (burst) probes.
func ThroughputConfig() Config {
	c := DefaultConfig()
	c.MinGoodputKBps = 64
	c.GoodputMargin = 32
	return c
}

// Decide advises the zapret rung from the baseline (direct, no desync) quality
// and the per-recipe trials. Pure: no I/O, deterministic. Preference:
// not-blocked (NoDesync) > a recipe that reliably beats baseline (Recipe) >
// rung unviable (Unviable — Brain escalates).
func Decide(baseline quality.Quality, trials []Trial, cfg Config) Verdict {
	gp := cfg.MinGoodputKBps > 0 // throughput-aware mode

	// 1. Not blocked: direct already works. In throughput mode "works" also
	//    requires sustained goodput — a frozen path connects with low loss but
	//    near-zero goodput, and must NOT read as healthy (the old blind spot).
	if baseline.Samples >= cfg.MinSamples && baseline.Loss <= cfg.BaselineOKLoss &&
		(!gp || baseline.GoodputKBps >= cfg.MinGoodputKBps) {
		return Verdict{Outcome: OutcomeNoDesync, Reason: "baseline (direct, no desync) is healthy — service not blocked"}
	}

	// 2. Among recipes that are trustworthy AND reliable AND measurably beat the
	//    baseline, keep the best. Beating baseline is what tells "the recipe
	//    helped" apart from "the site happened to work this time".
	best := -1
	for i := range trials {
		q := trials[i].Q
		if q.Samples < cfg.MinSamples {
			continue // too few samples to trust
		}
		if q.Loss > cfg.MaxRecipeLoss {
			continue // the recipe itself isn't reliable
		}
		if gp && q.GoodputKBps < cfg.MinGoodputKBps {
			continue // froze/throttled — desync can't beat an IP-volume-freeze
		}
		// Credited if it cuts loss by the margin OR (throughput mode) lifts goodput
		// by the margin — the freeze case improves goodput, not loss.
		beatsLoss := q.Loss <= baseline.Loss-cfg.LossMargin
		beatsGoodput := gp && q.GoodputKBps >= baseline.GoodputKBps+cfg.GoodputMargin
		if !beatsLoss && !beatsGoodput {
			continue
		}
		if best < 0 || better(q, trials[best].Q, gp) {
			best = i
		}
	}
	if best >= 0 {
		return Verdict{Outcome: OutcomeRecipe, RecipeID: trials[best].RecipeID,
			Reason: "recipe reliably beats the no-desync baseline"}
	}

	// 3. Blocked and nothing helps — the zapret rung can't hold it. Report the
	//    fact; Brain owns the escalation.
	return Verdict{Outcome: OutcomeUnviable, Reason: "service is blocked and no recipe beats baseline; zapret rung unviable"}
}

// Regressed reports whether `after` is materially worse than `before` for a
// BYSTANDER service: loss rose by >= LossMargin, or (throughput mode) goodput
// fell by >= GoodputMargin. The discord-regressed-by-the-composer lesson — a
// recipe that fixes its own service must not silently harm another. The caller
// ensures both readings have enough samples to trust.
func Regressed(before, after quality.Quality, cfg Config) bool {
	if after.Loss >= before.Loss+cfg.LossMargin {
		return true
	}
	if cfg.MinGoodputKBps > 0 && after.GoodputKBps <= before.GoodputKBps-cfg.GoodputMargin {
		return true
	}
	return false
}

// Guard is one bystander service measured before and after a candidate recipe is
// added to the composed ruleset.
type Guard struct {
	Service       string
	Before, After quality.Quality
}

// GuardOK reports whether a candidate recipe is safe to promote: no bystander
// regresses. Returns the first regressed service (for logging) and false when one
// does. Empty guard list is OK (nothing to harm).
func GuardOK(guards []Guard, cfg Config) (regressed string, ok bool) {
	for _, g := range guards {
		if Regressed(g.Before, g.After, cfg) {
			return g.Service, false
		}
	}
	return "", true
}

// better reports whether a is the stronger quality. In throughput mode goodput
// dominates (it's the objective), then loss, then the latency tail; otherwise
// loss dominates (reliability first), then the tail as the tie-breaker.
func better(a, b quality.Quality, gp bool) bool {
	if gp && a.GoodputKBps != b.GoodputKBps {
		return a.GoodputKBps > b.GoodputKBps
	}
	if a.Loss != b.Loss {
		return a.Loss < b.Loss
	}
	return a.P95ms < b.P95ms
}
