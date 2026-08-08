// Package brain is the policy: it decides what should be active for each
// service and emits desired state. It touches no network. Inputs are
// ProductionVerdict (from the prober) and ActualStateObserved (from Applier);
// output is DesiredStateChanged. It consults the KB synchronously to resolve
// chain steps whose strategy is chosen at runtime (e.g. ALT_ZAPRET).
//
// The escalate/recover algorithm follows the spec: asymmetric thresholds
// (leave fast, return slow), a settling window after escalation during which
// failures are ignored, and silent probes of lower positions to detect when a
// preferred strategy has recovered.
package brain

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/anomaly"
	"github.com/strace-me/lotsman/pkg/audit"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/policy"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// Smarts wires the intelligence layer into Brain's escalation decision. All
// fields are hooks the daemon supplies (closures over correlate/damper/adaptive/
// anomaly/kb), keeping Brain decoupled from those packages. When Smarts is nil,
// Brain uses the plain fail-count threshold (the original behavior).
type Smarts struct {
	FeedAnomaly   func(service string, ok bool, rttMs int) // record probe into the anomaly detector
	ObserveHealth func(service string, healthy bool)       // update the correlation detector
	Threshold     func(service, activeStrategy string) int // adaptive escalate threshold
	Systemic      func() bool                              // many services down at once
	FlapBackoff   func(service string, now time.Time) time.Duration
	Anomaly       func(service string) anomaly.State     // current anomaly state
	RecordSwitch  func(service string, now time.Time)    // note a transition for the flap damper
	SuggestClass  func(errText string, rttMs int) string // block-type -> mechanism class for escalation jumps
	// BlockType returns the raw observed block-type string (e.g. "tcp_reset") for a
	// probe failure, and BlockTypesFor returns the block-types a strategy is declared
	// to beat. Together they let zapret strategy resolution PREFER a strategy known
	// to beat the currently-observed block type (LOT-40). Both optional; nil = the
	// plain KB EWMA ordering (no block-type preference).
	BlockType     func(errText string, rttMs int) string
	BlockTypesFor func(strategyID string) []string
}

// SetSmarts enables the intelligence layer. Call before Run.
func (b *Brain) SetSmarts(s *Smarts) { b.smarts = s }

// PathOracle gives Brain the empirical health of a service's chain (escalation-v2).
// NextWorking returns the lowest position STRICTLY ABOVE `above` last seen HEALTHY
// (E-2, leave-fast escalation). RecoverTarget returns the lowest position STRICTLY
// BELOW `below` that has been STABLY healthy (E-4, return-slow recovery). Both
// return -1 if none/unknown. Brain uses them to jump straight to the best working
// tier instead of walking the chain one rung at a time.
type PathOracle interface {
	NextWorking(service string, above int) int
	RecoverTarget(service string, below int) int
}

// SetPathOracle enables empirical escalation jumps (E-2). Independent of Smarts;
// nil = original one-rung-at-a-time escalation. Call before Run.
func (b *Brain) SetPathOracle(o PathOracle) { b.pathOracle = o }

// Config holds the state-machine tunables. Production defaults follow the spec
// (asymmetric thresholds, 60s settling); tests and the demo shorten them.
type Config struct {
	EscalateFails  int           // consecutive active failures before escalating
	RecoverSuccess int           // consecutive silent successes before recovering
	SettlingWindow time.Duration // ignore active failures for this long after escalating
}

// DefaultConfig returns the spec values.
func DefaultConfig() Config {
	return Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 60 * time.Second}
}

// Recommender is the slice of the KB that Brain needs. Keeping it an interface
// makes Brain testable without a real KB and matches the "Brain uses KB
// through a contract, does not own it" rule. In a split deployment this
// becomes a request/reply over the bus.
type Recommender interface {
	TopNZapret(service string, n int, exclude ...string) []string
}

type runtime struct {
	svc                registry.Service
	position           int
	failsCurrent       int
	recSuccesses       map[int]int // lower position -> consecutive silent successes
	settlingUntil      time.Time
	broken             bool
	currentStrategy    string // resolved strategy id of the active step
	suggestedClass     string // mechanism class suggested by block-type detection
	suggestedBlockType string // raw observed block-type (e.g. "tcp_reset"); biases zapret strategy pick
	desiredPos         int
	actualPos          int // -1 = unknown
}

// Brain holds per-service runtime state and drives the state machine.
type Brain struct {
	bus        *events.Bus
	kb         Recommender
	cfg        Config
	audit      audit.Recorder
	smarts     *Smarts              // optional intelligence layer; nil = plain threshold
	pathOracle PathOracle           // optional empirical path-health (E-2); nil = one-rung escalation
	persist    func(map[string]int) // called under mu after each transition; may be nil
	log        *slog.Logger

	reassertEvery time.Duration // >0: periodically re-emit desired state so the applier re-converges the data plane

	mu       sync.Mutex
	runtimes map[string]*runtime
}

// SetReassert makes Brain periodically re-emit the current desired state for
// every service, so the applier re-converges the data plane toward it. This is
// the single mechanism that heals external drift — a sing-box restart that reset
// the selectors, or fresh node-ranker advice — without waiting for a state
// transition. It re-emits (idempotent at the applier) but does NOT record audit
// transitions or persist. 0 disables it (init-only emit). Call before Run.
func (b *Brain) SetReassert(every time.Duration) { b.reassertEvery = every }

// New builds a Brain over the registry. All services start at position 0;
// state is not persisted across restarts (the prober re-discovers reality
// within a couple of probe intervals).
func New(bus *events.Bus, reg *registry.Registry, kb Recommender, cfg Config, rec audit.Recorder, log *slog.Logger) *Brain {
	if rec == nil {
		rec = audit.Nop{}
	}
	rts := make(map[string]*runtime, len(reg.Services))
	for name, svc := range reg.Services {
		rts[name] = &runtime{
			svc:          svc,
			position:     0,
			recSuccesses: map[int]int{},
			desiredPos:   0,
			actualPos:    -1,
		}
	}
	return &Brain{bus: bus, kb: kb, cfg: cfg, audit: rec, log: log, runtimes: rts}
}

// Restore sets initial chain positions from persisted state (call before Run).
// Unknown services and out-of-range positions are ignored.
func (b *Brain) Restore(positions map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, pos := range positions {
		rt, ok := b.runtimes[name]
		if !ok || pos < 0 || pos >= len(rt.svc.Chain) {
			continue
		}
		rt.position = pos
		rt.desiredPos = pos
	}
}

// SetPersist registers a callback invoked (under the lock) after every
// transition with the current per-service positions, for state persistence.
func (b *Brain) SetPersist(fn func(map[string]int)) { b.persist = fn }

func (b *Brain) positionsLocked() map[string]int {
	out := make(map[string]int, len(b.runtimes))
	for name, rt := range b.runtimes {
		out[name] = rt.position
	}
	return out
}

// Position returns a service's current chain position. The prober reads this
// to decide which step to probe actively and which lower step to probe
// silently. Safe for concurrent use.
func (b *Brain) Position(service string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rt, ok := b.runtimes[service]; ok {
		return rt.position
	}
	return 0
}

// ServiceState is a point-in-time view of one service, for metrics/status.
type ServiceState struct {
	Service  string
	Position int
	State    string
	Fails    int
	Broken   bool
	// Strategy is the id Brain RESOLVED for this rung — which is not always what
	// the data plane ended up running. A zapret rung's id comes from the KB, and
	// the local engine may be unable to render it and fall back; surfacing the
	// asked-for value is what makes that divergence visible instead of silent.
	Strategy string
}

// Snapshot returns the current state of every service. Safe for concurrent use.
func (b *Brain) Snapshot() []ServiceState {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ServiceState, 0, len(b.runtimes))
	for _, rt := range b.runtimes {
		out = append(out, ServiceState{
			Service:  rt.svc.Name,
			Position: rt.position,
			State:    rt.svc.Chain[rt.position].State,
			Fails:    rt.failsCurrent,
			Broken:   rt.broken,
			Strategy: rt.currentStrategy,
		})
	}
	// Sorted, because runtimes is a MAP and Go randomises its iteration order on every
	// call. Callers legitimately treat this as a stable list: the desync composer turns
	// it into an ordered set of nfqws --new blocks, so a reshuffle made the composed
	// argv differ from the last one, which made Apply believe the strategy had changed
	// and kill+relaunch nfqws — measured at 78 restarts in two minutes, with the canary
	// then judging recipes against an engine that had just been restarted. The UI's
	// service list flapped for the same reason.
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// Run emits the initial desired state for every service, then processes
// verdicts and actual-state reports until ctx is cancelled.
func (b *Brain) Run(ctx context.Context) {
	b.mu.Lock()
	for _, rt := range b.runtimes {
		b.emitDesiredLocked(rt, "init")
	}
	b.mu.Unlock()

	var tick <-chan time.Time
	if b.reassertEvery > 0 {
		t := time.NewTicker(b.reassertEvery)
		defer t.Stop()
		tick = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case v := <-b.bus.Verdicts:
			b.onVerdict(v)
		case a := <-b.bus.ActualState:
			b.onActual(a)
		case <-tick:
			b.recoverViaOracle()
			b.reassert()
		}
	}
}

// recoverViaOracle is escalation-v2 E-4: parallel recovery. Instead of climbing
// back one silent rung at a time, it asks the path-health detector (which probes
// every tier each scan) for the lowest STABLY-healthy position below current and
// recovers straight to it. No-op unless a PathOracle is wired (-path-health-act).
func (b *Brain) recoverViaOracle() {
	if b.pathOracle == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for _, rt := range b.runtimes {
		if rt.position == 0 || rt.svc.Static {
			continue
		}
		// Anti-flap: a service in damper backoff (it switched recently) must NOT be
		// recovered yet. The path-health zapret-tier signal is box-direct and can be
		// over-optimistic vs the real LAN path (e.g. sing-box tls_fragment + nfqws):
		// without this gate an optimistic recovery fights the honest active probe and
		// oscillates. The damper bounds that to once per backoff window.
		if b.smarts != nil && b.smarts.FlapBackoff != nil && b.smarts.FlapBackoff(rt.svc.Name, now) > 0 {
			continue
		}
		target := b.pathOracle.RecoverTarget(rt.svc.Name, rt.position)
		if target < 0 || target >= rt.position {
			continue
		}
		b.log.Info("path-health recovery to best lower tier (parallel, skip one-rung climb)",
			"service", rt.svc.Name, "from_pos", rt.position, "to_pos", target)
		b.recoverToLocked(rt, target)
		if b.smarts != nil && b.smarts.RecordSwitch != nil {
			b.smarts.RecordSwitch(rt.svc.Name, now) // count recovery switches so the damper trips on a flap
		}
	}
}

// Reassert triggers an immediate re-converge of the data plane toward the
// current desired state. Safe to call from another goroutine (e.g. the node
// ranker after it refreshes its advice, so the new best node is applied at once
// instead of waiting for the periodic tick).
func (b *Brain) Reassert() { b.reassert() }

// ReassertService re-emits the desired state for a single service, so a caller
// that just refreshed an input for it (e.g. the ranker's best-node advice) gets
// it applied immediately — without re-emitting every service. Blocking delivery
// (the lock is released first) so the nudge is not dropped. No-op for unknown
// services; records no audit transition.
func (b *Brain) ReassertService(name string) {
	b.mu.Lock()
	rt, ok := b.runtimes[name]
	if !ok {
		b.mu.Unlock()
		return
	}
	step := rt.svc.Chain[rt.position]
	ev := events.DesiredStateChanged{
		Service:       rt.svc.Name,
		Position:      rt.position,
		State:         step.State,
		StrategyClass: step.StrategyClass,
		StrategyID:    rt.currentStrategy,
	}
	b.mu.Unlock()
	b.bus.DesiredState <- ev
}

// reassert re-sends the current desired state for every service so the applier
// re-converges actual selector state toward it. Non-blocking (a busy applier is
// retried next tick) and, unlike emitDesiredLocked, records no audit transition
// and does not persist — it asserts the SAME state, it is not a decision.
func (b *Brain) reassert() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, rt := range b.runtimes {
		step := rt.svc.Chain[rt.position]
		ev := events.DesiredStateChanged{
			Service:       rt.svc.Name,
			Position:      rt.position,
			State:         step.State,
			StrategyClass: step.StrategyClass,
			StrategyID:    rt.currentStrategy,
		}
		select {
		case b.bus.DesiredState <- ev:
		default: // applier busy; the next tick retries
		}
	}
}

func (b *Brain) onVerdict(v events.ProductionVerdict) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rt, ok := b.runtimes[v.Service]
	if !ok {
		return
	}

	if b.smarts != nil && b.smarts.FeedAnomaly != nil && v.Position == rt.position {
		b.smarts.FeedAnomaly(v.Service, v.OK, v.RTTms)
	}

	if v.Position == rt.position {
		// Liveness of the active strategy.
		if !v.OK && time.Now().Before(rt.settlingUntil) {
			b.log.Info("ignoring failure during settling window",
				"service", v.Service, "position", v.Position)
			return
		}
		if v.OK {
			if rt.failsCurrent > 0 || rt.broken {
				b.log.Info("active strategy healthy again",
					"service", v.Service, "position", v.Position, "cleared_fails", rt.failsCurrent)
			}
			rt.failsCurrent = 0
			rt.broken = false
			b.observeHealth(v.Service, true)
		} else {
			rt.failsCurrent++
			b.observeHealth(v.Service, false)
			if b.smarts != nil && b.smarts.SuggestClass != nil {
				rt.suggestedClass = b.smarts.SuggestClass(v.Err, v.RTTms)
			}
			if b.smarts != nil && b.smarts.BlockType != nil {
				rt.suggestedBlockType = b.smarts.BlockType(v.Err, v.RTTms)
			}
			if b.shouldEscalate(rt) {
				b.escalateLocked(rt)
				if b.smarts != nil && b.smarts.RecordSwitch != nil {
					b.smarts.RecordSwitch(v.Service, time.Now())
				}
			}
		}
		return
	}

	// Silent recovery probe of a lower (more preferred) position.
	if v.Position < rt.position {
		if v.OK {
			n := rt.recSuccesses[v.Position] + 1
			rt.recSuccesses[v.Position] = n
			recoverAt := b.cfg.RecoverSuccess
			if rt.svc.RecoverAfter > 0 {
				recoverAt = rt.svc.RecoverAfter
			}
			// A rule with nowhere left to escalate takes the FIRST measured success on
			// a lower rung. The consecutive-success threshold buys one thing — it stops
			// a WORKING service being dragged back onto a rung that is only
			// intermittently good — and a rule whose own rung is failing has no working
			// service to protect. Holding it to the same caution meant a service that
			// was down, on a path measured down, refused a path measured UP because the
			// measurement had only happened four times: escalation is forward-only
			// (chain exhausted logs BROKEN and stays put), so this probe is the only
			// road back, and it was gated as if the wait were free.
			if rt.broken {
				recoverAt = 1
			}
			b.log.Info("silent recovery progress",
				"service", v.Service, "probed_position", v.Position,
				"current_position", rt.position, "successes", n, "recover_at", recoverAt,
				"current_rung_broken", rt.broken)
			if n >= recoverAt {
				b.recoverToLocked(rt, v.Position)
			}
		} else if rt.recSuccesses[v.Position] > 0 {
			b.log.Info("silent recovery reset (probe failed)",
				"service", v.Service, "probed_position", v.Position)
			rt.recSuccesses[v.Position] = 0
		}
	}
}

// observeHealth feeds the correlation detector, if wired.
func (b *Brain) observeHealth(service string, healthy bool) {
	if b.smarts != nil && b.smarts.ObserveHealth != nil {
		b.smarts.ObserveHealth(service, healthy)
	}
}

// shouldEscalate decides whether an active-position failure escalates. Without
// Smarts it is the plain consecutive-fail threshold; with Smarts it runs the
// full policy (systemic/flap/anomaly aware) over adaptive thresholds.
func (b *Brain) shouldEscalate(rt *runtime) bool {
	// Static (nailed) services are never moved by the daemon — the operator pinned
	// them, so a failure is logged but the position holds.
	if rt.svc.Static {
		b.log.Info("active probe failed on static service — holding (nailed)",
			"service", rt.svc.Name, "position", rt.position, "fails", rt.failsCurrent)
		return false
	}

	// Per-service escalate_after wins over the adaptive hook and the global default.
	threshold := b.cfg.EscalateFails
	if rt.svc.EscalateAfter > 0 {
		threshold = rt.svc.EscalateAfter
	} else if b.smarts != nil && b.smarts.Threshold != nil {
		// Pass the active strategy directly (we hold the lock; the hook must not
		// call back into Brain or it self-deadlocks).
		threshold = b.smarts.Threshold(rt.svc.Name, rt.currentStrategy)
	}

	if b.smarts == nil {
		b.log.Info("active probe failed",
			"service", rt.svc.Name, "position", rt.position,
			"fails", rt.failsCurrent, "escalate_at", threshold)
		return rt.failsCurrent >= threshold
	}

	now := time.Now()
	in := policy.Inputs{
		ProbeOK:          false,
		ConsecutiveFails: rt.failsCurrent,
		EscalateAt:       threshold,
		InSettling:       now.Before(rt.settlingUntil),
	}
	if b.smarts.Systemic != nil {
		in.Systemic = b.smarts.Systemic()
	}
	if b.smarts.FlapBackoff != nil {
		in.FlapBackoff = b.smarts.FlapBackoff(rt.svc.Name, now)
	}
	if b.smarts.Anomaly != nil {
		in.Anomaly = b.smarts.Anomaly(rt.svc.Name)
	}
	d := policy.Decide(in)
	b.log.Info("escalation policy",
		"service", rt.svc.Name, "fails", rt.failsCurrent, "threshold", threshold,
		"action", string(d.Action), "reason", d.Reason)
	return d.Action == policy.Escalate
}

func (b *Brain) onActual(a events.ActualStateObserved) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rt, ok := b.runtimes[a.Service]
	if !ok {
		return
	}
	rt.actualPos = a.Position
	if rt.actualPos != rt.desiredPos {
		// Drift: production is not what we asked for. Re-assert desired.
		b.log.Warn("state drift, re-asserting desired",
			"service", a.Service, "actual", rt.actualPos, "desired", rt.desiredPos)
		b.emitDesiredLocked(rt, "reconcile_drift")
	}
}

func (b *Brain) escalateLocked(rt *runtime) {
	reason := "fails_threshold"
	next := rt.position + 1

	// E-2 empirical jump: the path-health detector probes every tier out-of-band,
	// so escalate straight to the lowest WORKING position above current — skipping
	// rungs it has already seen down. This is the parallel-failover fix for slow
	// sequential escalation (zapret→wait→fail→VPN→…). Same-class rungs share the
	// data path the detector probes, so a skipped zapret-alt was genuinely down; a
	// recipe that needs a strategy swap to come back is the v7-tuner's job, not
	// escalation's.
	if b.pathOracle != nil {
		if jump := b.pathOracle.NextWorking(rt.svc.Name, rt.position); jump > rt.position {
			if jump != next {
				b.log.Info("path-health jump to best working tier (skip known-down rungs)",
					"service", rt.svc.Name, "from_pos", rt.position, "to_pos", jump)
			}
			next = jump
			reason = "pathhealth_jump"
		}
	}

	// Block-type-aware jump (heuristic) — only when path-health gave no empirical
	// jump: if the detected block points at a mechanism (e.g. a timeout => VPN, an
	// RST => zapret), skip ahead to the next chain step of that class.
	if reason == "fails_threshold" && rt.suggestedClass != "" {
		for i := rt.position + 1; i < len(rt.svc.Chain); i++ {
			if rt.svc.Chain[i].StrategyClass == rt.suggestedClass {
				if i != next {
					b.log.Info("block-type jump",
						"service", rt.svc.Name, "suggested_class", rt.suggestedClass,
						"from_pos", rt.position, "to_pos", i)
					reason = "block_type:" + rt.suggestedClass
				}
				next = i
				break
			}
		}
	}

	if next >= len(rt.svc.Chain) {
		if !rt.broken { // log only on transition into BROKEN, not every probe
			rt.broken = true
			b.log.Error("all strategies exhausted", "service", rt.svc.Name, "state", registry.StateBroken)
		}
		return // stay on last position; recovery probes can still bring it back
	}
	rt.position = next
	rt.failsCurrent = 0
	rt.settlingUntil = time.Now().Add(b.cfg.SettlingWindow)
	b.emitDesiredLocked(rt, reason)
}

func (b *Brain) recoverToLocked(rt *runtime, pos int) {
	rt.position = pos
	rt.failsCurrent = 0
	rt.broken = false
	rt.recSuccesses = map[int]int{}
	b.emitDesiredLocked(rt, "silent_recovery")
}

// emitDesiredLocked resolves the current chain step (consulting the KB when the
// step's strategy is runtime-chosen) and publishes DesiredStateChanged. Caller
// holds b.mu.
func (b *Brain) emitDesiredLocked(rt *runtime, reason string) {
	step := rt.svc.Chain[rt.position]
	strategyID := b.resolveStrategyLocked(rt, step)
	rt.currentStrategy = strategyID
	b.audit.Record(audit.Transition{
		Service:       rt.svc.Name,
		FromPosition:  rt.desiredPos,
		ToPosition:    rt.position,
		State:         step.State,
		StrategyClass: step.StrategyClass,
		StrategyID:    strategyID,
		Reason:        reason,
	})
	rt.desiredPos = rt.position
	b.log.Info("desired state",
		"service", rt.svc.Name, "position", rt.position, "state", step.State,
		"class", step.StrategyClass, "strategy", strategyID, "reason", reason)
	b.bus.DesiredState <- events.DesiredStateChanged{
		Service:       rt.svc.Name,
		Position:      rt.position,
		State:         step.State,
		StrategyClass: step.StrategyClass,
		StrategyID:    strategyID,
	}
	if b.persist != nil {
		b.persist(b.positionsLocked())
	}
}

// resolveStrategyLocked returns the strategy ID for a step. A non-empty
// StrategyID is used as-is. An empty zapret step is resolved from the KB,
// excluding zapret strategies already used at lower positions so ALT_ZAPRET
// differs from PREFERRED.
func (b *Brain) resolveStrategyLocked(rt *runtime, step registry.ChainStep) string {
	if step.StrategyID != "" {
		return step.StrategyID
	}
	if step.StrategyClass == strategy.ClassZapret {
		exclude := make([]string, 0, len(rt.svc.Chain))
		for _, s := range rt.svc.Chain {
			if s.StrategyClass == strategy.ClassZapret && s.StrategyID != "" {
				exclude = append(exclude, s.StrategyID)
			}
		}
		top := b.kb.TopNZapret(rt.svc.Name, 16, exclude...)
		if len(top) == 0 {
			return ""
		}
		// BlockTypes preference (soft): float strategies declared to beat the
		// currently-observed block type to the front, preserving the KB's EWMA order
		// within each group. No observed type / no lookup / no match => unchanged
		// EWMA-best (LOT-40).
		if rt.suggestedBlockType != "" && b.smarts != nil && b.smarts.BlockTypesFor != nil {
			top = preferBlockType(top, rt.suggestedBlockType, b.smarts.BlockTypesFor)
		}
		return top[0]
	}
	return ""
}

// preferBlockType stable-reorders ids so strategies whose declared block-types
// include blockType come first (EWMA order kept within each group). Pure.
func preferBlockType(ids []string, blockType string, lookup func(string) []string) []string {
	match := make([]string, 0, len(ids))
	rest := make([]string, 0, len(ids))
	for _, id := range ids {
		hit := false
		for _, bt := range lookup(id) {
			if strings.EqualFold(strings.TrimSpace(bt), blockType) {
				hit = true
				break
			}
		}
		if hit {
			match = append(match, id)
		} else {
			rest = append(rest, id)
		}
	}
	return append(match, rest...)
}
