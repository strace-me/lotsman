// Package rulesets owns Track A of autoupdate: keeping the binary .srs rule-sets
// (runetfreedom et al.) fresh under a pin + autobump-with-validation policy. This
// file is the pure DECISION CORE — what to swap, what to keep, when to move the
// pin — reusing pkg/aggregate's shrink guard. The IO around it (download a
// release, decompile .srs to count entries, atomically swap files, trigger a
// reconcile) is box-adjacent and lives behind the interfaces below; the decisions
// here are fully unit-testable offline.
package rulesets

import (
	"sort"

	"github.com/strace-me/lotsman/pkg/aggregate"
)

// Counter returns the entry count in one compiled rule-set (.srs) file. The real
// implementation shells out to `sing-box rule-set decompile` (needs the box) and
// sums the match arrays; tests supply a fake. Counts feed the shrink guard. A
// missing/unreadable file should return (0, err) so the caller treats it as absent.
type Counter interface {
	Count(srsPath string) (int, error)
}

// ShrinkInfo records the counts behind a keep-old decision.
type ShrinkInfo struct {
	Prev, Next int
}

// Plan is the per-tag verdict for a candidate release versus the live rule-sets.
//
//	Swap     — tags whose new .srs should replace the live file.
//	KeepOld  — tags kept as-is because the candidate's count shrank below the
//	           guard (per-tag mode) or vanished from the release, or because the
//	           global guard rejected the whole release.
//	Missing  — required tags absent from BOTH candidate and live: the generated
//	           config would fail `sing-box check`, so the release is Unusable.
//	Unusable — true when Missing is non-empty; the caller must touch nothing.
type Plan struct {
	Swap     []string
	KeepOld  map[string]ShrinkInfo
	Missing  []string
	Unusable bool
}

// PlanSwap decides, per required rule-set tag, whether to swap in the candidate
// file or keep the live one.
//
//	required  — the rule-set tags the running config references (from services).
//	candidate — per-tag entry counts in the new release (absent key = not present).
//	live      — per-tag entry counts currently on disk (absent key = not present).
//	minRatio  — shrink guard: a tag (or the total, in global mode) is rejected when
//	            its new count drops below minRatio of the old. 0 disables the guard.
//	perTag    — true: evaluate each tag independently, so one legitimately-shrinking
//	            tag keeps its old file while the rest update. false (global): one
//	            verdict over the summed counts — reject the whole swap if the total
//	            shrank, otherwise update everything present.
//
// A required tag missing from the candidate but present on disk is kept (the
// release dropped it; we serve what we have). A required tag present in neither
// makes the release Unusable (nothing is changed).
func PlanSwap(required []string, candidate, live map[string]int, minRatio float64, perTag bool) Plan {
	p := Plan{KeepOld: map[string]ShrinkInfo{}}

	type cand struct {
		tag  string
		next int
		prev int
	}
	var swappable []cand
	var prevTotal, nextTotal int
	for _, tag := range required {
		next, inCand := candidate[tag]
		prev, inLive := live[tag]
		prevTotal += prev
		nextTotal += next
		switch {
		case !inCand && !inLive:
			p.Missing = append(p.Missing, tag)
		case !inCand && inLive:
			p.KeepOld[tag] = ShrinkInfo{Prev: prev, Next: 0} // vanished from release -> keep ours
		default: // present in candidate
			swappable = append(swappable, cand{tag: tag, next: next, prev: prev})
		}
	}
	if len(p.Missing) > 0 {
		p.Unusable = true
		sort.Strings(p.Missing)
		return p
	}

	if perTag {
		for _, c := range swappable {
			if aggregate.ShrinkOK(c.prev, c.next, minRatio) {
				p.Swap = append(p.Swap, c.tag)
			} else {
				p.KeepOld[c.tag] = ShrinkInfo{Prev: c.prev, Next: c.next}
			}
		}
	} else { // global: one verdict over the total
		if aggregate.ShrinkOK(prevTotal, nextTotal, minRatio) {
			for _, c := range swappable {
				p.Swap = append(p.Swap, c.tag)
			}
		} else {
			for _, c := range swappable {
				p.KeepOld[c.tag] = ShrinkInfo{Prev: c.prev, Next: c.next}
			}
		}
	}
	sort.Strings(p.Swap)
	return p
}

// NextPin implements the autobump-with-validation policy: only move the pin to a
// newer release that actually validated. current is the pinned release tag,
// latest is the newest upstream tag ("" or == current means nothing newer), and
// candidateUsable is whether the latest release passed PlanSwap (not Unusable).
// Returns the tag to pin going forward and whether it moved. Staying on a
// validated pin is the safe default — we never serve an unvalidated release.
func NextPin(current, latest string, candidateUsable bool) (pin string, bumped bool) {
	if latest == "" || latest == current || !candidateUsable {
		return current, false
	}
	return latest, true
}
