package core

import (
	"context"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// laneVerdictTTL is how long a lane answer about an inactive desync rung stands
// before it must be measured again. Long enough that the ten-second probe tick
// reuses one measurement many times, short enough that a network which changed
// under us is not described by a stale one.
const laneVerdictTTL = 3 * time.Minute

// laneProber answers "would this rule work on its desync rung?" by PROVING a
// recipe in the isolated lane, and it exists because nothing else could answer
// that question honestly.
//
// The bare-direct prober measures the path a rule would take with NO desync,
// because while the rule sits on VPN nfqws holds no profile for its domains. On
// this network that path dies at the TLS handshake, so the silent recovery probe
// said "no" forever and YouTube stayed on the tunnel — while the sandbox, four
// separate times, had already measured a recipe that carried it. Two halves that
// both worked and never spoke: the recovery decision listened to a probe without
// the recipe, and the only thing that could measure WITH the recipe was wired
// only to rotation.
//
// Answers are cached for laneVerdictTTL because a lift costs an nft table and an
// engine start, while the probe tick is every ten seconds. Between measurements
// it reports Unmeasured rather than repeating itself: five consecutive successes
// are what recovery asks for, and five echoes of one measurement are not five
// measurements — the same mistake the canary's standing verdict once made.
type laneProber struct {
	core *Core
	// fallback answers for rungs the lane cannot speak about — a direct/LOCKED
	// rung has no recipe to prove, so a bound direct probe is the right question
	// there.
	fallback dataplane.Prober

	mu    sync.Mutex
	cache map[string]laneVerdict
}

type laneVerdict struct {
	at   time.Time
	ok   bool
	why  string
	seen bool
}

func newLaneProber(c *Core, fallback dataplane.Prober) *laneProber {
	return &laneProber{core: c, fallback: fallback, cache: map[string]laneVerdict{}}
}

func (l *laneProber) Probe(ctx context.Context, service string, position int) events.ProductionVerdict {
	v := events.ProductionVerdict{Service: service, Position: position}
	svc, ok := l.core.reg.Services[service]
	if !ok || position < 0 || position >= len(svc.Chain) || svc.Chain[position].StrategyClass != strategy.ClassZapret {
		if l.fallback != nil {
			return l.fallback.Probe(ctx, service, position)
		}
		v.Unmeasured = true
		v.Err = "no prober for this rung"
		return v
	}

	if cached, fresh := l.fresh(service); fresh {
		v.OK = cached.ok
		if !cached.ok {
			v.Err = "the lane could not prove a recipe for this rung: " + cached.why
		}
		return v
	}
	if !l.core.claimRotation(service) {
		// Inside the cooldown with nothing fresh to report. Saying "no" here would
		// reset a recovery that a later measurement might have earned, and saying
		// "yes" would invent one.
		v.Unmeasured = true
		v.Err = "no fresh lane measurement for this rung yet"
		return v
	}

	cand, proven := l.core.provenCandidateFor(ctx, svc, "", false, false)
	res := laneVerdict{at: time.Now(), seen: true, ok: cand != "" && proven}
	if !res.ok {
		res.why = "no candidate carried the volume"
	}
	l.mu.Lock()
	l.cache[service] = res
	l.mu.Unlock()

	if res.ok {
		l.core.log.Info("lane proved a desync recipe for a rule that is not on it — recovery can proceed",
			"service", service, "position", position, "candidate", cand)
	}
	v.OK = res.ok
	if !res.ok {
		v.Err = "the lane could not prove a recipe for this rung: " + res.why
	}
	return v
}

func (l *laneProber) fresh(service string) (laneVerdict, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.cache[service]
	return v, ok && v.seen && time.Since(v.at) < laneVerdictTTL
}
