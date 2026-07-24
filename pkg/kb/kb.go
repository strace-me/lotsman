// Package kb is the strategy memory. In M0 it is in-memory only: it tracks an
// EWMA success rate per (service, strategy) from production verdicts and ranks
// strategies by that learned rate, falling back to the builtin cold-start seed
// order for strategies it has not yet observed. No SQLite/event-sourcing yet.
package kb

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/strace-me/lotsman/pkg/strategy"
)

// alpha weights fresh samples in the EWMA: 10% new, 90% history.
const alpha = 0.1

// prior is the assumed success rate for a never-observed (service, strategy).
const prior = 0.5

// exploreC scales the D-UCB exploration bonus in TopNZapret (LOT-41). The EWMA is
// a recency-weighted estimate (the right non-stationary mean), but greedy ranking
// alone under-explores: a strategy whose estimate decayed, or one never tried,
// must occasionally be re-checked so a working alternative is found after a
// failure and a previously-good one is re-validated. Deliberately EXPLOIT-leaning
// (0.2, below the bandit-textbook ~0.5): on a censorship router, abandoning a
// CONFIRMED-good strategy to try an unknown is costly, so a strongly-proven arm
// must stay on top; the bonus only flips the pick when the incumbent is mediocre
// (rate near the prior) or a tie. (Garivier-Moulines D-UCB; reward in [0,1].)
const exploreC = 0.2

// Circuit breaker (LOT-41 inc3, goal "exclude known-non-working"). After
// breakerThreshold consecutive failures a strategy is quarantined — TopNZapret
// stops returning it so the chain doesn't waste a rung on a proven-dead pick.
// Quarantine lifts after breakerCooldownTicks Decay ticks (the wall-clock proxy,
// ~1h at a 15m check-interval): the strategy goes half-open and is re-probed once;
// a fresh success closes the breaker, another failure re-opens it. A single
// success at any point resets the failure streak.
const breakerThreshold = 3
const breakerCooldownTicks = 4

// KB is the strategy knowledge base.
type KB struct {
	mu     sync.Mutex
	ewma   map[string]float64 // success-rate EWMA, key: service|strategyID
	rtt    map[string]float64 // latency EWMA (ms), updated only on success
	jitter map[string]float64 // EWMA of |rtt - rttEWMA| (ms), connection stability
	count  map[string]float64 // observation count per key, for the D-UCB exploration bonus (LOT-41)
	cfail  map[string]int     // consecutive failures per key, for the circuit breaker (LOT-41 inc3)
	quar   map[string]int     // remaining cooldown ticks; >0 = strategy quarantined (LOT-41 inc3)
	seed   []string           // cold-start ranking of zapret strategy IDs (what TopNZapret may return)
}

// New returns an empty KB seeded with the builtin cold-start order.
func New() *KB {
	return &KB{
		ewma:   make(map[string]float64),
		rtt:    make(map[string]float64),
		jitter: make(map[string]float64),
		count:  make(map[string]float64),
		cfail:  make(map[string]int),
		quar:   make(map[string]int),
		seed:   strategy.BuiltinZapretSeed,
	}
}

// record is the on-disk shape of one (service, strategy)'s learned quality.
type record struct {
	EWMA   float64 `json:"ewma"`
	RTT    float64 `json:"rtt"`
	Jitter float64 `json:"jitter"`
	Count  float64 `json:"count,omitempty"`       // observation count (D-UCB bonus, LOT-41)
	CFail  int     `json:"consec_fail,omitempty"` // consecutive failures (circuit breaker, LOT-41 inc3)
	Quar   int     `json:"quarantine,omitempty"`  // remaining cooldown ticks (circuit breaker, LOT-41 inc3)
}

type persisted struct {
	Records map[string]record `json:"records"`
}

// Save writes the learned records (EWMA/RTT/jitter per service|strategy) to path
// atomically, so accumulated experience survives a restart. The seed is NOT
// persisted — it is bounded by the strategy catalog at startup, not learned.
func (k *KB) Save(path string) error {
	k.mu.Lock()
	recs := make(map[string]record, len(k.ewma))
	for key, e := range k.ewma {
		recs[key] = record{EWMA: e, RTT: k.rtt[key], Jitter: k.jitter[key], Count: k.count[key], CFail: k.cfail[key], Quar: k.quar[key]}
	}
	k.mu.Unlock()

	data, err := json.Marshal(persisted{Records: recs})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load merges saved records into the KB. A missing file is a cold start (nil,
// not an error); a corrupt file returns an error so the operator notices. The
// seed set by SetZapretSeed is left untouched.
func (k *KB) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var in persisted
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for key, r := range in.Records {
		k.ewma[key] = r.EWMA
		k.rtt[key] = r.RTT
		k.jitter[key] = r.Jitter
		k.count[key] = r.Count
		k.cfail[key] = r.CFail
		k.quar[key] = r.Quar
	}
	return nil
}

// SetZapretSeed replaces the cold-start ranking TopNZapret falls back to and,
// crucially, bounds which strategy IDs it may return. Set this from the strategy
// catalog so the KB never proposes a strategy that has no launcher on disk.
func (k *KB) SetZapretSeed(ids []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seed = ids
}

// Stats is the learned connection quality for a (service, strategy).
type Stats struct {
	Success  float64 // EWMA success rate [0,1]
	Loss     float64 // 1 - Success
	RTTms    float64 // latency EWMA (0 = unseen)
	JitterMs float64 // RTT variability EWMA (0 = unseen)
	Count    float64 // observations behind Success (0 = unseen)
	Seen     bool
}

// Stats returns the learned metrics for a (service, strategy).
func (k *KB) Stats(service, strategyID string) Stats {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := service + "|" + strategyID
	s, seen := k.ewma[key]
	if !seen {
		s = prior
	}
	return Stats{
		Success:  s,
		Loss:     1 - s,
		RTTms:    k.rtt[key],
		JitterMs: k.jitter[key],
		Count:    k.count[key],
		Seen:     seen,
	}
}

// Reload atomically REPLACES the whole knowledge base with the contents of path
// (empty if the file does not exist — a cold start for a not-seen-before network).
// Unlike Load, which merges into the existing maps, this discards what was there:
// it is how the client swaps to another network's KB on the move without carrying
// the previous network's learning across. The file is read before the lock so a
// concurrent reader never observes a half-populated store.
func (k *KB) Reload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var in persisted
	if len(data) > 0 {
		if err := json.Unmarshal(data, &in); err != nil {
			return err
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ewma = make(map[string]float64, len(in.Records))
	k.rtt = make(map[string]float64, len(in.Records))
	k.jitter = make(map[string]float64, len(in.Records))
	k.count = make(map[string]float64, len(in.Records))
	k.cfail = make(map[string]int, len(in.Records))
	k.quar = make(map[string]int, len(in.Records))
	for key, r := range in.Records {
		k.ewma[key] = r.EWMA
		k.rtt[key] = r.RTT
		k.jitter[key] = r.Jitter
		k.count[key] = r.Count
		k.cfail[key] = r.CFail
		k.quar[key] = r.Quar
	}
	return nil
}

// NetworkPrior aggregates a strategy's observed success across every OTHER
// service in this KB — a per-network prior, since the client keeps one KB per
// network (-kb-dir). It answers "how well has this recipe done here, for anything
// else?", so a service that has never tried it can start from the network's
// experience rather than blind catalog order. exceptService is excluded so a
// service never primes its own prior. n is the total weight behind the average
// (0 = no other service has tried it — no prior available).
func (k *KB) NetworkPrior(strategyID, exceptService string) (success, n float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	suffix := "|" + strategyID
	var wsum, csum float64
	for key, e := range k.ewma {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		if strings.TrimSuffix(key, suffix) == exceptService {
			continue
		}
		c := k.count[key]
		if c <= 0 {
			c = 1 // recorded at least once even if the count did not persist
		}
		wsum += e * c
		csum += c
	}
	if csum == 0 {
		return 0, 0
	}
	return wsum / csum, csum
}

// TopNZapret returns up to n zapret strategy IDs for a service, ranked by their
// learned EWMA success rate (descending), with the builtin seed order as the
// tiebreak. Unobserved strategies carry the prior (0.5), so a cold-start KB
// returns pure seed order; as strategies prove or fail in production they rise
// or sink. exclude skips strategies already used higher in the chain.
func (k *KB) TopNZapret(service string, n int, exclude ...string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}

	k.mu.Lock()
	type cand struct {
		id      string
		seedIdx int
		rate    float64
		n       float64 // observation count (D-UCB)
	}
	var totalN float64
	build := func(honorQuarantine bool) []cand {
		out := make([]cand, 0, len(k.seed))
		totalN = 0
		for i, id := range k.seed {
			if skip[id] {
				continue
			}
			key := service + "|" + id
			if honorQuarantine && k.quar[key] > 0 {
				continue // circuit breaker: skip a quarantined (proven-dead) strategy (LOT-41 inc3)
			}
			n := k.count[key]
			out = append(out, cand{id: id, seedIdx: i, rate: k.rateLocked(service, id), n: n})
			totalN += n
		}
		return out
	}
	cands := build(true)
	// Safety: never strand the chain. If the breaker quarantined every eligible
	// strategy, fall back to ranking them anyway — a known-bad pick beats none.
	if len(cands) == 0 {
		cands = build(false)
	}
	k.mu.Unlock()

	// D-UCB ranking (LOT-41): score = EWMA rate + exploration bonus. At cold start
	// (totalN==0) the bonus is 0 for all, so this is exactly the old rate+seed
	// order. Once some strategies are observed, an unseen/low-count one gets a
	// bonus so it is tried — finding a working alternative after a failure and
	// re-validating a previously-good one. n+1 avoids the sqrt(1/0) blow-up.
	logT := math.Log(1 + totalN)
	score := func(c cand) float64 { return c.rate + exploreC*math.Sqrt(logT/(c.n+1)) }
	sort.SliceStable(cands, func(i, j int) bool {
		si, sj := score(cands[i]), score(cands[j])
		if si != sj {
			return si > sj // higher score (rate + exploration) first
		}
		return cands[i].seedIdx < cands[j].seedIdx // tiebreak: seed (blockcheck) order
	})

	out := make([]string, 0, n)
	for _, c := range cands {
		if len(out) >= n {
			break
		}
		out = append(out, c.id)
	}
	return out
}

// Decay multiplies every observation count by factor (0<factor<1) — the D-UCB
// freshness mechanism (LOT-41, "рабочая раньше ≠ рабочая сейчас"). Counts shrink
// over wall-time when a strategy is NOT re-tried, so its exploration bonus rises
// and it gets re-validated; the learned EWMA rate is left intact (the forced
// re-probe refreshes it — gentler than blindly forgetting the rate). Call
// periodically (the daemon ticks it each check-interval). No-op for out-of-range
// factor.
func (k *KB) Decay(factor float64) {
	if factor <= 0 || factor >= 1 {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for key := range k.count {
		k.count[key] *= factor
	}
	// One Decay tick is also one circuit-breaker cooldown tick (LOT-41 inc3): count
	// down quarantines and release (half-open) the ones that reach zero.
	for key := range k.quar {
		k.quar[key]--
		if k.quar[key] <= 0 {
			delete(k.quar, key)
		}
	}
}

// rateLocked returns the EWMA for (service, strategyID), or the prior if unseen.
// Caller holds k.mu.
func (k *KB) rateLocked(service, strategyID string) float64 {
	if v, ok := k.ewma[service+"|"+strategyID]; ok {
		return v
	}
	return prior
}

// RecordOutcome folds a probe result into the success EWMA for
// (service, strategyID) and, on success, into the latency EWMA. Returns the
// updated success rate. A never-seen pair starts at the prior.
func (k *KB) RecordOutcome(service, strategyID string, ok bool, rttMs int) float64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := service + "|" + strategyID
	cur, seen := k.ewma[key]
	if !seen {
		cur = prior
	}
	sample := 0.0
	if ok {
		sample = 1.0
	}
	cur = alpha*sample + (1-alpha)*cur
	k.ewma[key] = cur
	k.count[key]++ // sample count feeds the D-UCB exploration bonus (LOT-41)

	// Circuit breaker (LOT-41 inc3): a success resets the failure streak and lifts
	// any quarantine; breakerThreshold consecutive failures quarantine the strategy.
	if ok {
		k.cfail[key] = 0
		delete(k.quar, key)
	} else {
		k.cfail[key]++
		if k.cfail[key] >= breakerThreshold {
			k.quar[key] = breakerCooldownTicks
		}
	}

	if ok && rttMs > 0 {
		if r, seen := k.rtt[key]; seen {
			// Jitter = EWMA of deviation from the current latency baseline,
			// measured before the baseline moves.
			dev := float64(rttMs) - r
			if dev < 0 {
				dev = -dev
			}
			if j, ok := k.jitter[key]; ok {
				k.jitter[key] = alpha*dev + (1-alpha)*j
			} else {
				k.jitter[key] = dev
			}
			k.rtt[key] = alpha*float64(rttMs) + (1-alpha)*r
		} else {
			k.rtt[key] = float64(rttMs)
		}
	}
	return cur
}

// Score ranks a (service, strategy) by success rate, lightly penalized by
// latency, so a fast working strategy outranks a slow working one while success
// stays dominant. Range roughly [0,1]; unseen pairs score at the prior.
func (k *KB) Score(service, strategyID string) float64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := service + "|" + strategyID
	success, seen := k.ewma[key]
	if !seen {
		success = prior
	}
	// Latency penalty: up to ~0.3 for very slow paths, capped so it never
	// flips a clearly-better success rate.
	penalty := 0.0
	if r, ok := k.rtt[key]; ok {
		penalty = r / 1000.0
		if penalty > 0.3 {
			penalty = 0.3
		}
	}
	return success - penalty
}

// Snapshot returns a copy of all (service|strategy -> EWMA) entries, for
// metrics export and the audit log.
func (k *KB) Snapshot() map[string]float64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]float64, len(k.ewma))
	for key, v := range k.ewma {
		out[key] = v
	}
	return out
}
