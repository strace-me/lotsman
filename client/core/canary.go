package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/burstprobe"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// canaryDelay lets the freshly applied strategy take effect before it is judged.
// A verdict taken immediately would mostly measure the restart.
const canaryDelay = 3 * time.Second

// judge applies the verdict for a composed desync strategy to the KB, which is
// what turns the picker from a fixed catalog order into something that learns.
//
// This closes the loop the catalog cannot close on its own. Measured live: the
// catalog holds both flowseal-general-multisplit-568-4pda, which does nothing
// against this DPI, and flowseal-general-fake-multisplit-664-max, which fixes it
// completely — and they sit one line apart. A cold KB scores every candidate the
// same, so the picker falls back to catalog order, takes the first, fails, and
// has no way to ever find out; it would keep choosing the broken one forever with
// the working one immediately behind it.
//
// Recording the outcome against the RECIPE id is what lets KBPicker demote a
// strategy that lost and promote one that won, per service.
func (z *zapretExec) judge(ctx context.Context, service string, chosen map[string]string) {
	if z.canary == nil {
		return
	}
	recipe := chosen[service]
	if recipe == "" {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(canaryDelay):
	}

	// The brain may have moved the service off the desync rung during the settle
	// window — the third consecutive failure escalates on the very tick that spawned
	// this judge. Probing then measures the tunnel it moved to and would file that
	// as the RECIPE's success, promoting the strategy whose failures caused the
	// escalation. Verify the rung still holds before forming any opinion.
	if !z.stillOnRung(service) {
		z.log.Info("desync canary: service left the rung during the settle window, no verdict",
			"service", service, "recipe", recipe)
		return
	}
	ok := z.canary(ctx, service)
	z.record(service, recipe, ok)
	z.noteCarrying(service, ok)
	if ok {
		z.log.Info("desync canary: strategy works", "service", service, "recipe", recipe)
		return
	}
	// A losing strategy is demoted rather than retried: the next Enable re-composes
	// and the picker, now seeing a worse score for this recipe, reaches for the
	// next candidate.
	z.log.Warn("desync canary: strategy did not restore the service, demoting it",
		"service", service, "recipe", recipe)
}

// noteCarrying records the canary's verdict where the probe engine can consult it.
//
// Demoting the recipe was never enough on its own. Demotion only reorders the
// PICKER; whether the service stays on the desync rung at all is the brain's
// decision, and the brain listens to the active probe — which fetches a couple of
// hundred bytes and is therefore blind to exactly the failure the canary measures.
// Observed on the ThinkPad: youtube sat on a recipe the canary scored at 0 KB/s
// ninety consecutive times while every probe returned ok in ~57ms, so the service
// never escalated and the demotion changed nothing.
func (z *zapretExec) noteCarrying(service string, carrying bool) {
	z.carryMu.Lock()
	defer z.carryMu.Unlock()
	if z.notCarrying == nil {
		z.notCarrying = map[string]bool{}
	}
	z.notCarrying[service] = !carrying
}

// StallReason reports that the desync rung this service sits on is up but not
// carrying, and why — the probing engine's stall-oracle contract.
//
// Scoped to services CURRENTLY on a desync rung, deliberately. The verdict is
// about a recipe; once the brain has escalated, the service is on a tunnel this
// canary never measured, and continuing to fail its probe would walk it off a path
// nobody has any evidence against.
//
// The verdict is CONSUMED, not held. Left standing it failed every subsequent
// probe until the next canary happened to pass, so the probe stopped being a
// second opinion and became an echo: the three consecutive failures escalation
// asks for could all originate in ONE canary verdict. Measured on the ThinkPad —
// 151 probe failures in twenty minutes carried this reason, driving 37
// escalations. One canary verdict now fails one probe, so three failures mean
// three canaries actually said so.
func (z *zapretExec) StallReason(service string) (string, bool) {
	if !z.stillOnRung(service) {
		return "", false
	}
	z.carryMu.Lock()
	defer z.carryMu.Unlock()
	if !z.notCarrying[service] {
		return "", false
	}
	delete(z.notCarrying, service)
	return "desync canary measured no goodput on this recipe: the path connects but carries nothing", true
}

// stillOnRung reports whether the service is STILL on a zapret rung.
func (z *zapretExec) stillOnRung(service string) bool {
	if z.active == nil {
		return false
	}
	for _, svc := range z.active() {
		if svc.Name == service {
			return true
		}
	}
	return false
}

// canaryProbe reports whether the service is reachable right now, probing the
// rung it currently sits on. It reuses the prober the autonomy loop already
// drives, so a canary verdict and a routine verdict mean the same thing.
func (c *Core) canaryProbe(ctx context.Context, service string) bool {
	svc, ok := c.reg.Services[service]
	if !ok || c.prober == nil {
		return false
	}
	pos := 0
	for _, step := range svc.Chain {
		if step.StrategyClass == strategy.ClassZapret {
			pos = step.Position
			break
		}
	}
	if !c.prober.Probe(ctx, service, pos).OK {
		return false
	}
	return c.goodputOK(ctx, svc)
}

// goodputOK is the canary's second stage: does the recipe let real VOLUME
// through, not merely a status line?
//
// Reachability alone cannot tell a working desync from one that connects and
// then crawls, and TSPU's characteristic failure is exactly that — the handshake
// completes and the flow freezes a few tens of kilobytes in. A canary that only
// asks "did it connect" therefore scores a frozen path as a win and teaches the
// KB to prefer it. The shallow probe still runs first because it is cheap: on a
// cold KB the recipe changes roughly every ten seconds, and pulling volume for
// every candidate that cannot even connect would be waste.
//
// Disabled (CanaryGoodputKBps <= 0) it is a no-op and the canary means exactly
// what it meant before.
func (c *Core) goodputOK(ctx context.Context, svc registry.Service) bool {
	min := c.opts.CanaryGoodputKBps
	if min <= 0 {
		return true
	}
	// A dedicated bulk URL when the operator gave one; the probe target otherwise,
	// with the short-endpoint guard below to stop it producing a fake verdict.
	target := svc.VolumeTarget
	if target == "" {
		target = svc.ProbeTarget
	}
	if target == "" || !strings.HasPrefix(target, "http") {
		return true // nothing to pull volume from; the shallow verdict stands
	}
	// Not while someone is playing. A burst probe pulls real bytes down the very
	// uplink it is measuring, so running it mid-session both spoils the session
	// and mismeasures the path. Skipping costs one uncredited recipe; not
	// skipping costs the household's game.
	if dataplane.RealtimeActive(ctx, c.clash) {
		c.log.Info("canary: live UDP session, not pulling volume", "service", svc.Name)
		return true
	}
	q := burstprobe.Probe(ctx, dataplane.BurstClient(c.opts.ProbeProxy, 15*time.Second),
		[]string{target}, c.opts.CanaryGoodputBytes, 1)
	if q.Samples == 0 {
		return true // the measurement did not happen; do not invent a verdict
	}
	// The endpoint ran out before we had pulled enough to judge. Then the goodput
	// figure describes how small the URL is, not how bad the path is, and blaming
	// the strategy for it is the exact defect this project keeps finding.
	//
	// This was not hypothetical. youtube's probe target is `generate_204` — a
	// response with NO BODY — so its throughput canary returned 0 KiB/s on every
	// strategy ever applied and demoted all of them; the knowledge base ended up
	// with every zapret recipe for youtube at ewma 0 after ninety consecutive
	// "failures" that measured nothing. x's target is robots.txt, a few hundred
	// bytes, which scored a similarly meaningless 4.6 KiB/s.
	if q.Short {
		c.log.Warn("canary: cannot judge volume, the probe target has less to give than we ask for — set a volume_target for this service",
			"service", svc.Name, "target", target, "bytes", q.Bytes, "want_bytes", c.opts.CanaryGoodputBytes)
		return true
	}
	ok := q.GoodputKBps >= min
	if !ok {
		// Say which of the two happened. The fetch either failed outright — nothing
		// was delivered and nothing timed itself — or it connected and crawled, and
		// those want different next moves from whoever reads the log. The old line
		// asserted "connects" over both, which was untrue exactly when the fetch had
		// not connected at all: the same claim-about-the-unobserved this whole
		// function exists to stop making.
		if q.Bytes == 0 && q.Loss >= 1 {
			c.log.Info("canary: the volume fetch did not complete at all — no bytes, no response",
				"service", svc.Name, "target", target, "min_kbps", min)
		} else {
			c.log.Info("canary: recipe connects but does not carry volume",
				"service", svc.Name, "goodput_kbps", q.GoodputKBps, "min_kbps", min)
		}
	}
	return ok
}

// carryingFloorBytes is how much a rule must have MOVED since the previous
// observation pass to count as demonstrably working. One interval of real use
// clears it easily (a voice call is hundreds of KB); a keepalive or a failed
// handshake does not.
const carryingFloorBytes = 64 << 10

// carryingFloorPerFlow is the same question asked per CONNECTION, and it is the
// one that matters. An aggregate is trivially cleared by a browser thrashing:
// measured on the ThinkPad while YouTube would not load, the eye saw 1110 KiB
// across 387 live flows — 2.9 KiB each — which is not health, it is the freeze
// signature, a client opening connection after connection and getting a few
// kilobytes out of each before it dies. The aggregate floor waved that through.
//
// Above the ~16 KiB point where the TSPU volume freeze bites, so a flow that got
// past it counts and a flow that died at it does not.
const carryingFloorPerFlow = 24 << 10

// carryingStalledMax is the share of a rule's flows that may be frozen while it
// still counts as carrying. A path where most flows are stuck mid-stream is the
// freeze signature, and calling that "working" because the remaining flows still
// move bytes is exactly the false green this whole seam exists to prevent.
const carryingStalledMax = 0.5

// CarryingReason reports that a rule is demonstrably moving real traffic right
// now, from PASSIVE observation of the operator's own connections — the probing
// engine's activity-oracle contract.
//
// This is evidence of a different kind from a probe: it is the actual service
// being used, not a synthetic fetch of one URL over one protocol. It exists
// because the probe was measured wrong in both directions on the same machine
// within a day, and this is the half that costs a working session: Discord's
// gateway URL timed out for minutes while 3.5 MB of voice flowed through the same
// rule, and escalating on that would have torn down the call.
//
// It requires MOVEMENT since the last pass, not merely the existence of flows: a
// long-lived connection whose byte counter stopped advancing looks identical to a
// busy one in a single snapshot, and treating it as healthy would be the
// unobserved claim this project keeps finding.
func (c *Core) CarryingReason(service string) (string, bool) {
	snap := c.observeSnapshot()
	m, ok := snap.Services[service]
	if !ok || m.Flows == 0 {
		return "", false
	}
	if m.StalledRatio > carryingStalledMax {
		return "", false // most of it is frozen; movement elsewhere does not redeem that
	}
	c.carriedMu.Lock()
	if c.lastCarried == nil {
		c.lastCarried = map[string]int64{}
	}
	prev, seen := c.lastCarried[service]
	c.lastCarried[service] = m.Bytes
	c.carriedMu.Unlock()
	// The first observation has nothing to compare against; a cumulative total is
	// not evidence of movement.
	if !seen || m.Bytes <= prev {
		return "", false
	}
	moved := m.Bytes - prev
	if moved < carryingFloorBytes {
		return "", false
	}
	// Per flow, not just in total. Many connections each carrying a trickle is the
	// freeze, and summing them turns the symptom into the evidence.
	if perFlow := moved / int64(m.Flows); perFlow < carryingFloorPerFlow {
		return "", false
	}
	return fmt.Sprintf("%d KiB moved across %d live flows since the last pass (%d KiB each)",
		moved>>10, m.Flows, (moved/int64(m.Flows))>>10), true
}
