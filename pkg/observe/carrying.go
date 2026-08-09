package observe

import (
	"fmt"
	"sync"
)

// Carrying answers "is this service demonstrably moving real traffic RIGHT NOW",
// from passive observation of the user's own connections.
//
// It is the counterweight to the stall oracle, and without it that oracle has no
// opposition. A synthetic probe is wrong in both directions: it calls a path
// healthy while it carries nothing — which the eye's frozen-flow detector fixes —
// and it calls a path broken while the user is watching a video on it. On
// 2026-08-09 the router held YouTube on a working desync rung, the eye read the
// player's abandoned parallel range requests as "5 of 7 flows frozen", five such
// verdicts escalated the rule to VPN, and it flapped three times in ten minutes
// while the owner watched an uninterrupted video and 40 MB moved per window.
// The daemon had the stall oracle armed and this half missing.
//
// It lives here, beside the metrics it reads, because BOTH front-ends need it and
// the desktop client had the only copy.
type Carrying struct {
	// FloorBytes is how much a rule must have moved since the previous pass to
	// count. One interval of real use clears it easily; a keepalive does not.
	FloorBytes int64
	// FloorPerFlow is the same question asked per CONNECTION, and it is the
	// load-bearing one. Many connections each carrying a trickle IS the freeze, so
	// summing them turns the symptom into the evidence (principle 11). Set above
	// the ~16 KiB point where the TSPU volume freeze bites, so a flow that got past
	// it counts and a flow that died at it does not.
	FloorPerFlow int64
	mu           sync.Mutex
	last         map[string]int64
}

// DefaultCarrying is the tuning both front-ends use, so a rule is not judged
// carrying on one box and stalled on the other.
func DefaultCarrying() *Carrying {
	return &Carrying{FloorBytes: 64 << 10, FloorPerFlow: 24 << 10}
}

// Reason reports whether the service is visibly carrying, and the evidence.
//
// It measures the DELTA since the previous call, never the cumulative total: a
// counter that has been large since boot says nothing about now, and the first
// observation has nothing to compare against.
func (c *Carrying) Reason(service string, m ServiceMetrics) (string, bool) {
	if m.Flows == 0 {
		return "", false
	}
	// There was a third gate here — refuse when more than half the flows look
	// frozen — and it made this oracle useless exactly when it was needed. It is
	// the counterweight to the eye's stall verdict, and it stood down whenever the
	// eye was loudest, on the eye's own inference. Two signals that both derive
	// from one ratio are not two signals.
	//
	// Measured on the owner's router while he watched YouTube on the TV stick: the
	// eye reported "7 of 10 flows frozen", the veto disqualified itself on that
	// same ratio, and it fired zero times in an hour of uninterrupted video.
	//
	// It was also redundant. The two floors below already tell the cases apart FROM
	// BYTES: a real freeze is ten flows thrashing at ~3 KiB each, which fails both;
	// a video player is seven abandoned range requests beside three delivering
	// megabytes, which passes both — and the per-flow divisor is total flows, so the
	// abandoned ones already dilute the average rather than being ignored.
	c.mu.Lock()
	if c.last == nil {
		c.last = map[string]int64{}
	}
	prev, seen := c.last[service]
	c.last[service] = m.Bytes
	c.mu.Unlock()
	if !seen || m.Bytes <= prev {
		return "", false
	}
	moved := m.Bytes - prev
	if moved < c.FloorBytes {
		return "", false
	}
	perFlow := moved / int64(m.Flows)
	if perFlow < c.FloorPerFlow {
		return "", false
	}
	// The stalled ratio is REPORTED, not obeyed. It is real evidence — a rule whose
	// flows are mostly stuck while one big transfer masks it deserves a second look
	// — but it is an inference, and the decision follows the bytes. A human reading
	// the log sees both and can tell the two apart; the code does not pretend the
	// ratio settles it.
	if m.StalledRatio > 0 {
		return fmt.Sprintf("%d KiB moved across %d live flows since the last pass (%d KiB each; %.0f%% of flows look frozen)",
			moved>>10, m.Flows, perFlow>>10, m.StalledRatio*100), true
	}
	return fmt.Sprintf("%d KiB moved across %d live flows since the last pass (%d KiB each)",
		moved>>10, m.Flows, perFlow>>10), true
}
