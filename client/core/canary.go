package core

import (
	"context"
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
// nobody has any evidence against. That is the same
// verdict-about-something-unmeasured this project keeps finding, and it would be
// self-inflicted.
func (z *zapretExec) StallReason(service string) (string, bool) {
	if !z.stillOnRung(service) {
		return "", false
	}
	z.carryMu.Lock()
	defer z.carryMu.Unlock()
	if !z.notCarrying[service] {
		return "", false
	}
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
	target := svc.ProbeTarget
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
	ok := q.GoodputKBps >= min
	if !ok {
		c.log.Info("canary: recipe connects but does not carry volume",
			"service", svc.Name, "goodput_kbps", q.GoodputKBps, "min_kbps", min)
	}
	return ok
}
