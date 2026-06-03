// Package policy composes Lotsman's signals into a single escalation decision.
// It is the "smart" core: rather than escalating on raw failure count alone, it
// weighs whether the failure is systemic (don't churn — upstream's fault),
// whether the service is settling or flapping (hold — give it time), and only
// then escalates. Pure and deterministic, so the decision logic is fully
// testable; Brain feeds it live signals and acts on the verdict.
package policy

import (
	"time"

	"github.com/strace-me/lotsman/pkg/anomaly"
)

// Action is what Brain should do with the active position.
type Action string

const (
	Stay     Action = "stay"     // healthy, keep current strategy
	Hold     Action = "hold"     // unhealthy but do not switch yet (and why)
	Escalate Action = "escalate" // move to the next fallback
)

// Inputs are the live signals for one service at decision time.
type Inputs struct {
	ProbeOK          bool
	Anomaly          anomaly.State
	Systemic         bool          // many services down at once (upstream cause)
	ConsecutiveFails int           // active-probe failures in a row
	EscalateAt       int           // failures needed to escalate
	InSettling       bool          // within the post-switch settling window
	FlapBackoff      time.Duration // extra hold imposed by the flap damper
}

// Decision is the policy verdict plus a machine-readable reason for logs/audit.
type Decision struct {
	Action Action
	Reason string
}

// Decide returns the action for a service given its current signals.
func Decide(in Inputs) Decision {
	if in.ProbeOK {
		if in.Anomaly == anomaly.Degraded {
			// Reachable but degraded (latency/jitter): keep current, surface it.
			return Decision{Stay, "degraded_watch"}
		}
		return Decision{Stay, "healthy"}
	}

	// Probe failed. Decide whether switching would actually help.
	switch {
	case in.Systemic:
		// Everything is down — a strategy switch won't fix an upstream outage,
		// and would waste the fallback chain. Wait it out.
		return Decision{Hold, "systemic_outage"}
	case in.InSettling:
		return Decision{Hold, "settling_window"}
	case in.FlapBackoff > 0:
		return Decision{Hold, "flap_backoff"}
	case in.ConsecutiveFails >= in.EscalateAt:
		return Decision{Escalate, "fails_threshold"}
	default:
		return Decision{Hold, "accumulating_fails"}
	}
}
