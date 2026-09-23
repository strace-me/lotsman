// Package noderank picks the best concrete VPN node FOR A SPECIFIC SERVICE and
// pins that service's selector to it. It exists to kill the core pain: sing-box
// url-test and client balancers (Hiddify/mihomo) rank nodes purely by latency
// to a generic probe URL (generate_204), so they happily pick a 2ms RU relay
// that sits behind the same TSPU, or a node whose QUIC/throughput is in fact
// dead. noderank instead:
//
//   - narrows candidates by exit country BEFORE probing — a blocked service is
//     never pinned to a RU exit, even if its ping is lowest (the Hiddify bug);
//   - probes each survivor through the SERVICE'S OWN url (youtube/discord/…),
//     so the ranking reflects the path the user actually cares about, not an
//     unrelated generate_204;
//   - pins the winner into sel-<svc> and otherwise leaves it alone — no flapping
//     to a worse node by raw ping.
//
// It is the opt-in "orchestrator" upgrade over the per-pool url-test default;
// with it off, sel-<svc> just rides its url-test pool like a dumb switcher.
package noderank

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/quality"
)

// API is the slice of the Clash client noderank needs (real impl:
// *dataplane.ClashClient). Narrow so tests can fake it.
type API interface {
	Proxy(ctx context.Context, name string) (dataplane.ProxyInfo, error)
	NodeDelay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error)
	SetSelector(ctx context.Context, selector, target string) error
}

// Candidate is a concrete node noderank may pin: its sing-box outbound tag and
// exit country (ISO-2 lowercase; "" = unknown, e.g. a provider "smart
// location" with no flag in its name).
type Candidate struct {
	Tag     string
	Country string
}

// Service is one ranking job: which selector to pin, what URL to probe through,
// how to weigh the result, and the country policy for eligible exits.
type Service struct {
	Name     string           // for logs
	Selector string           // sel-<svc>, the selector to pin
	ProbeURL string           // service-aware probe target (http(s) URL)
	Weights  balancer.Weights // category profile (balancer.ProfileFor)
	// Goodput measures a node's sustained throughput, in KiB/s. Optional: nil
	// keeps the latency-only ranking, which cannot distinguish a fast node from
	// a frozen one. The implementation is expected to refuse — ok=false — while
	// the link is carrying a live session, since pulling volume competes with
	// the very traffic it would disturb.
	Goodput   func(ctx context.Context, node string) (kbps float64, ok bool)
	ExcludeCC []string // exit countries to never pin (e.g. ["ru"]); unknown country is never excluded
	IncludeCC []string // if non-empty, ONLY pin these (e.g. ["us"] for Netflix); unknown country fails this filter
	// Sticky pins the service to its currently-advised node as long as that node
	// stays healthy and a selector member — never flipping by latency/score, only
	// failing over on real failure (the node leaving the pool). For multi-connection
	// sessions (voice/games) that a node change would disrupt. LOT-12 (right-sized:
	// per-service, not per-client).
	Sticky bool
}

// nodeHealth carries per-node hysteresis state across Pick cycles: how many
// probes in a row failed, and the last good quality to fall back on while a node
// is merely degraded (not yet down).
type nodeHealth struct {
	consecFail int
	lastGood   quality.Quality
	hasGood    bool
	// Last measured carry, kept separately from lastGood because it is recorded
	// even for a node the probe judged unhealthy — an exit that answers and moves
	// nothing is precisely the case worth showing, and lastGood would drop it.
	lastCarry  float64
	carryKnown bool
}

// downAfter is how many consecutive failed probes mark a node DOWN (evicted). A
// node that fails fewer than this — but was healthy before — is DEGRADED: kept as
// a (deprioritized) candidate so one bad probe never throws away a good node.
const downAfter = 3

// defaultSwitchMargin is the score delta a NEW node must beat the currently-
// advised node by before advice flips (anti-flap, à la mihomo url-test
// tolerance). Scores are in [0,1]; 0.05 ignores ~5% noise between near-equal
// nodes so a service does not ping-pong between two good exits every cycle.
const defaultSwitchMargin = 0.05

// defaultScreenURL is the cheap generic target for the screen phase. It is a
// generate_204 endpoint (tiny, no body), reachable from any live exit, so a
// failure here means the node is dead — not that the service path is bad.
const defaultScreenURL = "http://www.gstatic.com/generate_204"

// defaultScreenTimeout bounds the screen probe. Much tighter than the full
// per-service timeout: a dead node should fail fast so we never spend the
// (slower, multi-sample) service probe budget on it.
const defaultScreenTimeout = 1500 * time.Millisecond

// Ranker probes and pins. One instance serves all services and keeps per-node
// health between cycles (so it must be created once and reused).
type Ranker struct {
	api     API
	samples int
	timeout time.Duration
	dryRun  bool
	log     *slog.Logger

	// Goodput measures a node's sustained throughput in KiB/s. Optional: nil
	// keeps the latency-only ranking, which cannot distinguish a fast node from a
	// frozen one — under TSPU's volume freeze a node answers a delay test in 40ms
	// and then carries nothing, so RTT alone crowns the deadest exit in the pool.
	//
	// The implementation is expected to refuse (ok=false) while the link carries
	// a live session: pulling volume competes with the traffic it would disturb,
	// and a stale ranking for one interval is cheaper than a spoiled game.
	Goodput func(ctx context.Context, node string) (kbps float64, ok bool)

	// MinGoodputKBps is the floor applied to whatever the Goodput hook reports.
	//
	// KEEP IT 0 WHEN Goodput IS PASSIVE. Passive carry measures DEMAND: it cannot
	// tell "carried little because nobody asked" from "carried little because the
	// exit is throttled", and demoting on that reading is exactly the mistake LOT-52
	// cost nine minutes of false verdicts to learn — social was declared
	// TSPU-throttled while Instagram was serving it 392 KiB. The floor for measured
	// CAPACITY is CanaryFloorKBps, which acts only on canary samples.
	//
	// 0 disables it. A measurement counts only when GoodputKnown and not Short:
	// youtube's probe target is a 204 with no body, and reading its 0 KiB/s as a
	// verdict once demoted every strategy ever tried.
	MinGoodputKBps float64

	// CanaryFloorKBps demotes an exit whose CANARY carried less than this — a
	// volume we asked for, through a selector we aimed, so a low number is about
	// the path and not about how busy the operator was. Separate from
	// MinGoodputKBps on purpose: same shape of number, opposite trustworthiness,
	// and one knob covering both is how the passive one would get switched on by
	// accident. 0 disables it.
	CanaryFloorKBps float64

	// SwitchMargin is the minimum score advantage (in [0,1]) a new top node
	// must have over the currently-advised node before advice flips. While the
	// advised node stays healthy and within this margin, it is kept (stickiness).
	// If it goes DOWN/ineligible, advice switches regardless. Defaults to
	// defaultSwitchMargin; set <0 to disable (always take the top node).
	SwitchMargin float64

	// ScreenURL / ScreenTimeout configure the cheap first phase: a single
	// short-timeout delay check against a generic URL to drop obviously-dead
	// nodes before the full per-service probe runs. ScreenTimeout <= 0 or an
	// empty ScreenURL disables screening (every node goes straight to the full
	// probe — the pre-2-phase behaviour).
	ScreenURL     string
	ScreenTimeout time.Duration

	// Canary measures ONE node's CAPACITY by pulling a volume we chose through it,
	// after pointing the probe selector at it. Distinct from Goodput, which observes
	// how much a node happened to carry: that measures DEMAND, and an exit nobody
	// asked much of is not a slow exit. Only a transfer we sized measures the path.
	//
	// nil keeps the latency ranking exactly as it was — a half-wired canary would be
	// worse than none (principle 4).
	Canary func(ctx context.Context, service, node string) (kbps float64, ok bool)

	// CanaryEvery is how stale a node's measurement may be before it is re-run, and
	// PromoteMargin how far a challenger must exceed the incumbent before advice
	// moves. Zero uses the defaults in canary.go.
	CanaryEvery   time.Duration
	PromoteMargin float64

	cmu    sync.Mutex
	canary map[string]canarySample

	mu     sync.Mutex
	health map[string]*nodeHealth

	amu    sync.Mutex
	advice map[string]string // service -> recommended best node tag ("" = no recommendation)
}

// New builds a Ranker. samples is delay probes per node (>1 yields a jitter
// signal). The Ranker is ADVISORY: it probes nodes and records a best-node
// recommendation per service (see Best); it never writes a selector itself.
// Brain's applier is the single writer of the data plane and consults Best when
// it applies a VPN step, so the ranker can never override a non-VPN decision.
func New(api API, samples int, dryRun bool, log *slog.Logger) *Ranker {
	if samples < 1 {
		samples = 1
	}
	return &Ranker{
		api: api, samples: samples, timeout: 5 * time.Second, dryRun: dryRun, log: log,
		SwitchMargin:  defaultSwitchMargin,
		ScreenURL:     defaultScreenURL,
		ScreenTimeout: defaultScreenTimeout,
		health:        map[string]*nodeHealth{},
		advice:        map[string]string{},
	}
}

// Best returns the ranker's current best-node recommendation for a service, or
// "" if it has none (no eligible node, all down, or never ranked). The VPN
// executor uses this to pick the concrete node when Brain applies a VPN step.
func (r *Ranker) Best(service string) string {
	r.amu.Lock()
	defer r.amu.Unlock()
	return r.advice[service]
}

func (r *Ranker) setAdvice(service, node string) {
	r.amu.Lock()
	r.advice[service] = node
	r.amu.Unlock()
}

// observe updates a node's health with a fresh probe result and returns the
// quality to rank it by, whether it is currently healthy (this probe succeeded),
// and whether it is DOWN (evict). A degraded node (recent failure, but was good
// and not yet down) ranks on its last-good quality so a transient blip does not
// drop it.
func (r *Ranker) observe(tag string, q quality.Quality) (eff quality.Quality, healthy, down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.health[tag]
	if h == nil {
		h = &nodeHealth{}
		r.health[tag] = h
	}
	// Record the carry before any verdict below can return early: this is the one
	// number that distinguishes a working exit from a frozen one, and it must be
	// visible even — especially — for the node that is about to be demoted.
	if q.GoodputKnown {
		h.lastCarry, h.carryKnown = q.GoodputKBps, true
	}
	// An exit that ANSWERS but does not CARRY is down, and it is down on the
	// observation rather than after a streak: this is the TSPU volume freeze, where
	// a node replies to a delay probe in 40ms and then moves nothing. Latency,
	// jitter and loss all read healthy through it — that is the whole reason this
	// dimension exists (LOT-67).
	if r.MinGoodputKBps > 0 && q.GoodputKnown && !q.Short && q.GoodputKBps < r.MinGoodputKBps {
		h.consecFail++
		r.log.Warn("noderank: exit answers but carries nothing — demoted",
			"node", tag, "kbps", q.GoodputKBps, "floor", r.MinGoodputKBps, "p95ms", q.P95ms)
		return q, false, true
	}
	if q.Loss < 1 { // at least one probe attempt succeeded
		h.consecFail = 0
		h.lastGood = q
		h.hasGood = true
		return q, true, false
	}
	h.consecFail++
	if h.consecFail >= downAfter || !h.hasGood {
		return q, false, true // sustained failure, or never seen good -> down
	}
	return h.lastGood, false, false // degraded: keep last-good, not evicted
}

// NodeHealth is a read-only per-node health view (LOT-6).
type NodeHealth struct {
	Node       string
	State      string // "healthy" | "degraded" | "down"
	ConsecFail int
	// GoodputKBps is what this exit was last MEASURED carrying, and CarryKnown
	// says whether it was measured at all. The two are separate because "not
	// measured" and "measured as nothing" both render as 0 and mean opposite
	// things — the second is a frozen exit, the first is an idle one (LOT-67).
	GoodputKBps float64
	CarryKnown  bool
}

// HealthSnapshot returns the current per-node health noderank already tracks —
// the visibility gap that LOT-6 noted, surfaced FROM the ranker instead of a
// duplicate subscription Tracker. Same down/degraded thresholds as observe.
// Safe for concurrent reads. Sorted by node tag.
func (r *Ranker) HealthSnapshot() []NodeHealth {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]NodeHealth, 0, len(r.health))
	for tag, h := range r.health {
		state := "healthy"
		switch {
		case h.consecFail >= downAfter || !h.hasGood:
			state = "down"
		case h.consecFail > 0:
			state = "degraded"
		}
		out = append(out, NodeHealth{Node: tag, State: state, ConsecFail: h.consecFail,
			GoodputKBps: h.lastCarry, CarryKnown: h.carryKnown})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// Pick narrows cands by svc's country policy, probes survivors via svc.ProbeURL,
// ranks them by svc.Weights, and pins svc.Selector to the winner when it differs
// from the current pick and is a real member of the selector. Returns the chosen
// tag, or "" when nothing is pinnable (no eligible candidate, all down, or the
// winner is not a selector member). Never errors on a single bad node — only on
// a failure to read the selector itself.
func (r *Ranker) Pick(ctx context.Context, svc Service, cands []Candidate) (string, error) {
	// Remember the node we currently advise BEFORE clearing it: the switch
	// margin below keeps advising it (stickiness) unless a new node clearly
	// beats it or it is no longer eligible/healthy.
	prev := r.Best(svc.Name)
	// Clear first: if no node survives below, Best() returns "" and the VPN
	// executor falls back to the pool url-test (which finds a live node itself).
	r.setAdvice(svc.Name, "")
	eligible := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if countryOK(c.Country, svc.IncludeCC, svc.ExcludeCC) {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		r.log.Warn("noderank: no country-eligible nodes", "service", svc.Name,
			"candidates", len(cands), "exclude", svc.ExcludeCC, "include", svc.IncludeCC)
		return "", nil
	}

	info, err := r.api.Proxy(ctx, svc.Selector)
	if err != nil {
		return "", err
	}
	members := make(map[string]bool, len(info.All))
	for _, m := range info.All {
		members[m] = true
	}

	// Probe each survivor and fold in hysteresis: DOWN nodes are dropped, DEGRADED
	// nodes stay (on their last-good quality), HEALTHY nodes use the fresh probe.
	type scored struct {
		cand    balancer.Candidate
		healthy bool
	}
	// Probe all candidates CONCURRENTLY (bounded) — a large pool (e.g. 50+ VLESS
	// nodes) would otherwise take minutes serially, leaving the service on the
	// generic url-test pick that whole time. Each probe is an independent Clash
	// /delay call; hysteresis (observe) is folded in serially afterwards so the
	// per-node health state stays single-writer.
	raw := make([]quality.Quality, len(eligible))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, c := range eligible {
		wg.Add(1)
		go func(i int, tag string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Phase 1 (cheap screen): a single short-timeout delay against a
			// generic URL. If it fails the node is obviously dead, so we skip
			// the (slower, multi-sample) service probe entirely and record a
			// total-loss quality — existing hysteresis then degrades/evicts it
			// exactly as a full-probe failure would.
			if !r.screen(ctx, tag) {
				raw[i] = quality.FromRTTs(nil, r.samples)
				return
			}
			// Phase 2 (full): probe through the service's own URL.
			raw[i] = r.probe(ctx, tag, svc.ProbeURL)
		}(i, c.Tag)
	}
	wg.Wait()

	pool := make([]scored, 0, len(eligible))
	for i, c := range eligible {
		eff, healthy, down := r.observe(c.Tag, raw[i])
		if down {
			continue
		}
		pool = append(pool, scored{cand: balancer.Candidate{ID: c.Tag, Q: eff}, healthy: healthy})
	}
	if len(pool) == 0 { // every eligible node is down (sustained failure)
		r.log.Warn("noderank: all eligible nodes down for service", "service", svc.Name,
			"selector", svc.Selector, "eligible", len(eligible), "probe_url", svc.ProbeURL)
		return "", nil
	}
	// Healthy nodes outrank degraded ones; within a tier, rank by quality. So a
	// degraded (but not down) node only wins when nothing healthy is available —
	// keeping a blipping node usable without preferring it over a live one.
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].healthy != pool[j].healthy {
			return pool[i].healthy
		}
		return balancer.Score(pool[i].cand, svc.Weights) > balancer.Score(pool[j].cand, svc.Weights)
	})
	best := pool[0].cand

	// The measured half. One node per service per pass, serialised — never a
	// fan-out: more than three concurrent TLS handshakes provoke the very freeze
	// this is measuring for. Called from Pick, which the ranking loop drives on an
	// interval, so the measurement follows a steady state rather than an edge
	// (principle 14 — the previous throughput canary was only ever called from the
	// apply path and so could not see a path that broke while sitting still).
	shortlist := make([]string, 0, len(pool))
	for _, p := range pool {
		shortlist = append(shortlist, p.cand.ID)
	}
	r.runCanary(ctx, svc.Name, shortlist)
	if advised, decided := r.promote(svc.Name, best.ID, prev, shortlist); decided {
		for _, p := range pool {
			if p.cand.ID == advised {
				best = p.cand
				break
			}
		}
	}

	// Switch margin (anti-flap): if the previously-advised node is still in the
	// pool, healthy, and a selector member, only flip to a new top node when it
	// beats the advised node's score by more than SwitchMargin. Otherwise keep
	// the advised node (stickiness). A DOWN/ineligible advised node is not in
	// the pool, so we fall through and take the new top — the margin only guards
	// healthy-vs-healthy churn.
	if prev != "" && prev != best.ID && r.SwitchMargin >= 0 {
		for _, p := range pool {
			if p.cand.ID != prev || !p.healthy || !members[prev] {
				continue
			}
			topScore := balancer.Score(best, svc.Weights)
			prevScore := balancer.Score(p.cand, svc.Weights)
			// Sticky: never flip a healthy advised node by latency/score (only the
			// down/ineligible fall-through above fails it over). Else apply the margin.
			if svc.Sticky || topScore-prevScore <= r.SwitchMargin {
				best = p.cand // sticky or within margin: stay put
			}
			break
		}
	}

	if !members[best.ID] {
		// The generated selector does not list this concrete node, so a PUT would
		// 400. Surface it loudly — the config generator and the candidate set have
		// drifted (e.g. ranker ran with a pool the selector was not built for).
		r.log.Warn("noderank: winner not a selector member, cannot pin", "service", svc.Name,
			"selector", svc.Selector, "winner", best.ID)
		return "", nil
	}

	// Advisory only: record the recommendation. Brain's applier reads it (via
	// Best) when it applies a VPN step — the ranker never writes the selector,
	// so it can never override a zapret/direct decision.
	r.setAdvice(svc.Name, best.ID)
	r.log.Info("noderank: recommend node", "service", svc.Name, "selector", svc.Selector,
		"now", info.Now, "best", best.ID, "p95ms", best.Q.P95ms, "loss", best.Q.Loss,
		"kbps", r.carryFor(best.ID, best.Q))

	// The ranking is still by latency, so say when the evidence disagrees with it.
	// On 2026-08-12 the pool grew from 52 to 104 nodes, url-test re-voted on delay,
	// and youtube/github landed on an exit that answered in 103ms and broke every
	// TLS handshake — with nothing in any log to contradict the healthy-looking
	// numbers. This is that missing line. It states a fact and changes no decision:
	// promoting on it needs a challenger canary, not a louder log
	if best.Q.GoodputKnown {
		for _, p := range pool {
			if !p.cand.Q.GoodputKnown || p.cand.ID == best.ID {
				continue
			}
			if p.cand.Q.GoodputKBps > best.Q.GoodputKBps*carryDisagreement {
				r.log.Warn("noderank: the chosen exit is not the one carrying most",
					"service", svc.Name, "chosen", best.ID, "chosen_kbps", best.Q.GoodputKBps,
					"carrying_most", p.cand.ID, "carrying_most_kbps", p.cand.Q.GoodputKBps,
					"why", "ranking is by latency; measured volume does not promote yet (LOT-67)")
				break
			}
		}
	}
	return best.ID, nil
}

// screen is the cheap phase-1 liveness check: a single short-timeout delay
// against the generic ScreenURL. Returns true if the node answered (passes to
// the full probe) or if screening is disabled (empty URL / non-positive
// timeout — then every node passes through). A node that passes the screen but
// later fails the full service probe is still handled by the usual hysteresis.
func (r *Ranker) screen(ctx context.Context, node string) bool {
	if r.ScreenURL == "" || r.ScreenTimeout <= 0 {
		return true
	}
	_, err := r.api.NodeDelay(ctx, node, r.ScreenURL, r.ScreenTimeout)
	return err == nil
}

// probe runs samples delay tests through the service URL; failures count as loss.
//
// Latency alone cannot see the failure that matters most here. A node under
// TSPU's volume freeze answers a delay test in 40ms and then carries nothing
// past the first few tens of kilobytes, so a ranking built on RTT crowns the
// deadest exit in the pool. Goodput, when the caller supplies a way to measure
// it, is what tells those apart.
// carryDisagreement is how many times more an unchosen exit must be carrying
// before the log says the ranking and the evidence disagree. An order of
// magnitude: smaller gaps are ordinary (the exits carry different services), and
// a line that fires on noise is a line nobody reads.
const carryDisagreement = 10

// carryFor renders what is KNOWN about a node's carry, preferring the canary over
// the passive reading and saying which it is. Two different measurements answer two
// different questions — capacity we asked for, versus demand that happened — and a
// bare number that does not say which is a number nobody can act on.
//
// It reads the canary map rather than the probe's Quality because the canary runs
// beside the probe, not inside it: the first live pass logged "unmeasured" for an
// exit its own canary had measured at 311 KiB/s one line earlier.
func (r *Ranker) carryFor(tag string, q quality.Quality) string {
	if kbps, ok := r.measuredCarry(tag, time.Now()); ok {
		return strconv.FormatFloat(kbps, 'f', 1, 64) + " (canary)"
	}
	if q.GoodputKnown {
		return strconv.FormatFloat(q.GoodputKBps, 'f', 1, 64) + " (carried)"
	}
	return "unmeasured"
}

func (r *Ranker) probe(ctx context.Context, node, testURL string) quality.Quality {
	rtts := make([]float64, 0, r.samples)
	for i := 0; i < r.samples; i++ {
		if d, err := r.api.NodeDelay(ctx, node, testURL, r.timeout); err == nil {
			rtts = append(rtts, float64(d))
		}
	}
	q := quality.FromRTTs(rtts, r.samples)
	// Only for a node that answers at all: measuring volume through one that
	// cannot connect buys nothing and costs bytes.
	if r.Goodput != nil && q.Samples > 0 && q.Loss < 1 {
		if kbps, ok := r.Goodput(ctx, node); ok {
			q.GoodputKBps = kbps
			q.GoodputKnown = true
		}
	}
	return q
}

// countryOK applies the exit-country policy. Exclude wins over everything (but a
// node with unknown country is never excluded — a "smart location" with no flag
// must stay usable). Include, when set, is a hard allowlist: only listed
// countries pass, and unknown country fails it (you cannot prove a no-flag node
// is in the required country).
func countryOK(cc string, include, exclude []string) bool {
	cc = strings.ToLower(strings.TrimSpace(cc))
	if cc != "" {
		for _, x := range exclude {
			if cc == strings.ToLower(strings.TrimSpace(x)) {
				return false
			}
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, in := range include {
		if cc != "" && cc == strings.ToLower(strings.TrimSpace(in)) {
			return true
		}
	}
	return false
}
