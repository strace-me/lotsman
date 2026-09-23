package noderank

import (
	"context"
	"time"
)

// The canary is the active half of node ranking: it points a dedicated probe
// selector at ONE node and pulls a known volume through it, so the number
// describes that node rather than whatever path happened to be carrying.
//
// Why this exists at all. Delay is addressable — Clash /proxies/{node}/delay
// takes the node — but throughput is not, so the previous implementation pulled
// bytes down the active path and handed every candidate of a pass the same value
// (LOT-67). Passive observation fixed the addressing but answers a different
// question: bytes that crossed an exit measure DEMAND, not capacity. An exit that
// carried 0.4 Mbps because the operator was reading text is not a 0.4 Mbps exit.
// Only a transfer we ourselves sized measures capacity.
//
// The cost is accepted and paid slowly, per the owner: ONE node per service per
// pass, serialised, never in parallel. More than three concurrent TLS handshakes
// provoke the TSPU freeze — a fan-out here would manufacture the failure it is
// measuring for.
const (
	// defaultCanaryEvery is how stale a node's canary may be before it is worth
	// re-running. A result is evidence about a moment: the freeze is per-connection
	// and time-varying, so an old number is not a small error, it is a different
	// question. Past this age a node is treated as unmeasured rather than as its
	// last value.
	defaultCanaryEvery = 30 * time.Minute

	// defaultPromoteMargin is the fraction by which a challenger's measured carry
	// must exceed the incumbent's before the advice flips. Promotion on a hair's
	// difference is churn, and a switch costs a session.
	defaultPromoteMargin = 0.25
)

// canarySample is one measured capacity and when it was taken.
type canarySample struct {
	kbps float64
	at   time.Time
}

// fresh reports whether this sample is still evidence rather than history.
func (s canarySample) fresh(now time.Time, every time.Duration) bool {
	return !s.at.IsZero() && now.Sub(s.at) < every
}

// dueForCanary picks the ONE node to measure this pass, or "" when every
// candidate already has a fresh number. Preference order is deliberate:
//
//  1. a node with NO measurement at all — an unknown must not stay unknown, which
//     is how a cold start ends up handing the decision back to url-test;
//  2. otherwise the stalest, so attention spreads instead of re-measuring the
//     node that happens to sort first.
//
// The incumbent is a candidate like any other. Comparing a freshly-canaried
// challenger against an incumbent measured an hour ago is not a comparison, and
// the research is explicit that the honest form is canary against canary.
func (r *Ranker) dueForCanary(cands []string, now time.Time) string {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	var pick string
	var oldest time.Time
	for _, tag := range cands {
		s, seen := r.canary[tag]
		if !seen || s.at.IsZero() {
			return tag // never measured wins outright
		}
		if s.fresh(now, r.canaryEvery()) {
			continue
		}
		if oldest.IsZero() || s.at.Before(oldest) {
			pick, oldest = tag, s.at
		}
	}
	return pick
}

func (r *Ranker) canaryEvery() time.Duration {
	if r.CanaryEvery > 0 {
		return r.CanaryEvery
	}
	return defaultCanaryEvery
}

func (r *Ranker) promoteMargin() float64 {
	if r.PromoteMargin > 0 {
		return r.PromoteMargin
	}
	return defaultPromoteMargin
}

// runCanary measures one node, if one is due, and records the result. It is
// deliberately a no-op when no hook is wired: the latency ranking then behaves
// exactly as before rather than half-working.
func (r *Ranker) runCanary(ctx context.Context, service string, cands []string) {
	if r.Canary == nil || len(cands) == 0 {
		return
	}
	now := time.Now()
	tag := r.dueForCanary(cands, now)
	if tag == "" {
		return
	}
	kbps, ok := r.Canary(ctx, service, tag)
	if !ok {
		// A refusal is not a zero. The hook declines while a live session is on the
		// link, and recording that as "carries nothing" would demote a good exit for
		// the crime of being busy.
		r.log.Debug("noderank: canary declined", "service", service, "node", tag)
		return
	}
	r.cmu.Lock()
	if r.canary == nil {
		r.canary = map[string]canarySample{}
	}
	r.canary[tag] = canarySample{kbps: kbps, at: now}
	r.cmu.Unlock()
	r.log.Info("noderank: canary measured an exit", "service", service, "node", tag, "kbps", kbps)
}

// measuredCarry returns a node's canary result when it is still fresh. The bool
// is the whole point: "not measured" and "measured as nothing" render identically
// as 0 and mean opposite things — the first is an exit nobody has tested, the
// second is a frozen one.
func (r *Ranker) measuredCarry(tag string, now time.Time) (float64, bool) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	s, ok := r.canary[tag]
	if !ok || !s.fresh(now, r.canaryEvery()) {
		return 0, false
	}
	return s.kbps, true
}

// promote applies the measured half of the decision to the latency shortlist.
//
// This is deliberately NOT a term inside balancer.Score. Score averages over the
// terms it has data for, so a measured node and an unmeasured one would be scored
// on different term sets — the averages are not comparable, and whichever side of
// the fleet happened to be measured would move for reasons unrelated to quality.
// The comparison here is explicit and only ever canary against canary.
//
// Returns the tag to advise and whether the measurement changed anything.
func (r *Ranker) promote(service, latencyBest, incumbent string, shortlist []string) (string, bool) {
	if r.Canary == nil {
		return latencyBest, false
	}
	now := time.Now()

	// Demotion first, and it does not need a challenger: an exit MEASURED carrying
	// nothing is unusable regardless of who else is available. This is the floor the
	// passive path could never safely apply, because passive data cannot tell an idle
	// exit from a throttled one. A canary pulled a volume we chose, so a low number
	// is about the path.
	if r.CanaryFloorKBps > 0 {
		if kbps, ok := r.measuredCarry(latencyBest, now); ok && kbps < r.CanaryFloorKBps {
			for _, tag := range shortlist {
				if tag == latencyBest {
					continue
				}
				alt, ok := r.measuredCarry(tag, now)
				if ok && alt >= r.CanaryFloorKBps {
					r.log.Warn("noderank: the fastest exit carries nothing — advising a measured one instead",
						"service", service, "was", latencyBest, "was_kbps", kbps,
						"now", tag, "now_kbps", alt, "floor", r.CanaryFloorKBps)
					return tag, true
				}
			}
			r.log.Warn("noderank: the fastest exit carries nothing and no measured alternative exists",
				"service", service, "node", latencyBest, "kbps", kbps, "floor", r.CanaryFloorKBps)
		}
	}

	// Promotion needs BOTH sides measured. Without that it is a guess dressed as a
	// measurement, and the incumbent keeps its place — holding is the answer when
	// there is no evidence, never handing the decision back to latency.
	if incumbent == "" || incumbent == latencyBest {
		return latencyBest, false
	}
	inc, haveInc := r.measuredCarry(incumbent, now)
	chal, haveChal := r.measuredCarry(latencyBest, now)
	if !haveInc || !haveChal {
		return latencyBest, false
	}
	if chal > inc*(1+r.promoteMargin()) {
		r.log.Info("noderank: challenger carries materially more — advising it (the applier decides when it lands)",
			"service", service, "from", incumbent, "from_kbps", inc,
			"to", latencyBest, "to_kbps", chal, "margin", r.promoteMargin())
		return latencyBest, true
	}
	r.log.Debug("noderank: challenger did not beat the incumbent by the margin — holding",
		"service", service, "incumbent", incumbent, "incumbent_kbps", inc,
		"challenger", latencyBest, "challenger_kbps", chal)
	return incumbent, true
}
