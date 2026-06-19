// Package remediate is the self-heal remediation PLANNER (LOT-18a): a pure
// function that turns a misroute Verdict into a staged remediation Plan — one
// rung of the ladder from docs/DESIGN-selfheal-misroute.md §4.
//
// Phase 18a is PROPOSE-ONLY. Plan has no I/O and mutates nothing: it decides
// WHICH rung is appropriate for a verdict (given whether learned CDN CIDRs are
// available yet) and returns a description. The caller logs it and publishes a
// metric — it does NOT call the generator with the plan, trigger reconcile, or
// touch the live config. Auto-apply + canary/rollback and the between-rungs
// escalation state machine (try rung1, if still misrouted advance to rung2) are
// LOT-18b; the Plan struct is the seam that work arms.
//
// The ladder (for a misrouted service):
//
//	rung 1 — ip-fallback   : route the service's learned CDN CIDRs to its
//	                         sel-<svc> selector, so un-sniffed QUIC tunnels by IP
//	                         instead of leaking to direct. Preferred for a leak,
//	                         but only when CIDRs are available.
//	rung 2 — reject-quic   : a route rule rejecting the service's udp/443 so the
//	                         client falls back to TLS-over-TCP (which sniffs
//	                         reliably). Used for a dead verdict, or a leak when no
//	                         CIDRs are learned yet.
//	rung 3 — escalate-node : last resort (placeholder here; full impl in LOT-18b).
package remediate

import (
	"fmt"
	"net"

	"github.com/strace-me/lotsman/pkg/misroute"
)

// Action names the remediation a Plan proposes. ActionNone means the service is
// healthy and nothing should be done.
const (
	ActionNone         = "none"
	ActionIPFallback   = "ip-fallback"
	ActionRejectQUIC   = "reject-quic"
	ActionEscalateNode = "escalate-node"
)

// Inputs carries the per-service facts the planner needs beyond the verdict.
// It is injected so Plan stays pure and testable (no iplearn import required);
// InputsFromLearner is a convenience builder for callers that have a Learner.
type Inputs struct {
	// CIDRs holds the learned/available CDN CIDRs per service (keyed by service
	// name), the source for an ip-fallback rung. An empty/absent entry means no
	// CIDRs are learned yet, which forces a leak verdict down to reject-quic.
	CIDRs map[string][]*net.IPNet
}

// Plan is the planner's output for one service: the chosen rung and a
// machine-readable Action, plus the CIDRs an ip-fallback would use and a
// human-readable Reason. A healthy verdict yields Rung 0 / ActionNone. The
// struct is the contract LOT-18b consumes to actually apply (and canary) the
// remediation.
type Plan struct {
	Service string
	Rung    int    // 0 = none, 1 = ip-fallback, 2 = reject-quic, 3 = escalate-node
	Action  string // ActionNone | ActionIPFallback | ActionRejectQUIC | ActionEscalateNode
	CIDRs   []*net.IPNet
	Reason  string
}

// Decide picks the appropriate remediation rung for a verdict, returning the
// Plan to (eventually) apply. It is pure: no I/O, no mutation of v or in.
//
// (Named Decide, not Plan, because Plan is the return type — Go forbids a
// function and type sharing a name. It is the single entry point the design's
// "Plan(verdict)" refers to.)
//
// Selection is single-shot and staged from the verdict alone (the between-rungs
// escalation is LOT-18b):
//
//   - healthy verdict            -> rung 0, none.
//   - leak + CIDRs available      -> rung 1, ip-fallback (preferred: route the
//     un-sniffed QUIC to the tunnel by IP).
//   - leak + no CIDRs yet         -> rung 2, reject-quic (kill udp/443 so the
//     client retries over TCP, which sniffs).
//   - dead                        -> rung 2, reject-quic (QUIC is stalling; force
//     the TCP path).
//   - misrouted but unknown kind  -> rung 2, reject-quic (safe generic fallback).
func Decide(v misroute.Verdict, in Inputs) Plan {
	if !v.Misrouted {
		return Plan{Service: v.Service, Rung: 0, Action: ActionNone, Reason: "healthy: no remediation needed"}
	}

	cidrs := in.CIDRs[v.Service]

	// A stall is the TSPU IP-throttle: only a foreign-egress chain escalation
	// (stall oracle → failed active probe → Brain) can move off it. A route toggle
	// (reject-quic) or node-escalation placeholder cannot fix an IP-keyed throttle,
	// and re-deciding one every pass just loops on an unfixable condition (the
	// observed reject-quic/escalate-node incident storm). Emit no route remediation;
	// defer to the chain escalation (LOT-43).
	if v.Kind == misroute.KindStalled {
		return Plan{Service: v.Service, Rung: 0, Action: ActionNone,
			Reason: "stalled: TSPU IP-throttle — handled by foreign-egress chain escalation, no route remediation"}
	}

	if v.Kind == misroute.KindLeak && len(cidrs) > 0 {
		return Plan{
			Service: v.Service,
			Rung:    1,
			Action:  ActionIPFallback,
			CIDRs:   cidrs,
			Reason:  reasonIPFallback(v, len(cidrs)),
		}
	}

	return Plan{
		Service: v.Service,
		Rung:    2,
		Action:  ActionRejectQUIC,
		Reason:  reasonRejectQUIC(v),
	}
}

// EscalatePlan returns the rung-3 escalate-node plan for a service. It is the
// last-resort placeholder: LOT-18a only needs it reachable (so the rung exists
// as a seam); the actual node-escalation is wired in LOT-18b. Plan never selects
// rung 3 on its own — a caller advances to it once rungs 1 and 2 have failed.
func EscalatePlan(service string) Plan {
	return Plan{
		Service: service,
		Rung:    3,
		Action:  ActionEscalateNode,
		Reason:  "escalate-node: rungs 1-2 exhausted; escalate the service's node/pool (LOT-18b)",
	}
}

func reasonIPFallback(v misroute.Verdict, n int) string {
	return fmt.Sprintf("ip-fallback: leak %.2f; route %d learned CDN CIDR(s) to the service selector so un-sniffed QUIC tunnels by IP",
		v.LeakRatio, n)
}

func reasonRejectQUIC(v misroute.Verdict) string {
	switch v.Kind {
	case misroute.KindLeak:
		return fmt.Sprintf("reject-quic: leak %.2f but no CDN CIDRs learned yet; reject udp/443 to force TLS-over-TCP (sniffs reliably)",
			v.LeakRatio)
	case misroute.KindDead:
		return fmt.Sprintf("reject-quic: dead-flow %.2f; QUIC stalling, reject udp/443 to force the TCP path",
			v.DeadFlowRatio)
	default:
		return "reject-quic: misrouted; reject udp/443 to force TLS-over-TCP fallback"
	}
}
