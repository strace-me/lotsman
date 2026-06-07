// Package selector chooses which strategy to try next, the missing link
// between detection and action. Instead of walking the fallback chain blindly,
// it prefers the strategy class that addresses the detected block type (from
// tspu) and, within that, the candidate with the best learned score (success
// rate penalized by latency, from the KB). So "DPI reset detected" jumps to the
// best-performing zapret strategy rather than the next-in-line one.
//
// Deferred (LOT-40): build-ahead, no callers — SUPERSEDED in practice. Its two
// jobs are already done elsewhere: block-type→class jumping by brain.escalateLocked
// (the empirical/path-health jump + suggestedClass fallback), and learned score
// ranking by kb.TopNZapret / zaptune.KBPicker. Within-class block-type preference
// landed in brain.resolveStrategyLocked (preferBlockType). Keep as a clean
// reference impl; delete if it stays unused after the strategy-intelligence epic
// (LOT-10) settles.
package selector

// Candidate is a strategy available to switch to.
type Candidate struct {
	StrategyID string
	Class      string
}

// Select picks the best candidate. preferClass (e.g. tspu.SuggestClass) biases
// toward strategies of that class; if empty or none match, all candidates
// compete. score ranks candidates (higher is better, e.g. kb.Score). Returns
// false if there are no candidates.
func Select(candidates []Candidate, preferClass string, score func(strategyID string) float64) (Candidate, bool) {
	if len(candidates) == 0 {
		return Candidate{}, false
	}

	pool := candidates
	if preferClass != "" {
		var matching []Candidate
		for _, c := range candidates {
			if c.Class == preferClass {
				matching = append(matching, c)
			}
		}
		if len(matching) > 0 {
			pool = matching // honor the detected block type when we can
		}
	}

	best := pool[0]
	bestScore := score(best.StrategyID)
	for _, c := range pool[1:] {
		if s := score(c.StrategyID); s > bestScore {
			best, bestScore = c, s
		}
	}
	return best, true
}
