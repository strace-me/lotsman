package core

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/burstprobe"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/quality"
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
	ok, why := z.canary(ctx, service)
	z.record(service, recipe, ok)
	z.noteCarrying(service, ok, why)
	if ok {
		z.log.Info("desync canary: strategy works", "service", service, "recipe", recipe)
		return
	}
	z.log.Warn("desync canary: strategy did not restore the service, demoting it",
		"service", service, "recipe", recipe)
	// Demotion alone was never enough, and once automatic recovery from VPN was
	// removed as dishonest it became nothing at all: a demoted score only matters
	// the next time the rule ENTERS the rung, and a rule that fails here escalates
	// away and does not come back. So the rule tried exactly one recipe per
	// session — guess wrong, ride the tunnel forever. Measured on the owner's
	// laptop as all eleven rules on VPN within an hour.
	//
	// Try the next candidates in the SANDBOX instead, and move the rule's live
	// traffic only onto one that passed. Testing before switching is the whole
	// point: the operator should never be the one who finds out a recipe is wrong.
	if z.rotate != nil {
		z.rotate(ctx, service, recipe)
	}
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
func (z *zapretExec) noteCarrying(service string, carrying bool, why string) {
	z.carryMu.Lock()
	defer z.carryMu.Unlock()
	if z.notCarrying == nil {
		z.notCarrying = map[string]string{}
	}
	if carrying {
		delete(z.notCarrying, service)
		return
	}
	if why == "" {
		why = "the desync canary failed for this recipe"
	}
	z.notCarrying[service] = why
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
// The third value is always true: this verdict comes from the canary, which
// pulled the rule's OWN volume target and counted what arrived. That is a fact
// about this rule's path, and it outranks the activity veto — unlike the eye's
// ratio, which is a suspicion and must not.
func (z *zapretExec) StallReason(service string) (string, bool, bool) {
	if !z.stillOnRung(service) {
		return "", false, false
	}
	z.carryMu.Lock()
	defer z.carryMu.Unlock()
	why, pending := z.notCarrying[service]
	if !pending {
		return "", false, false
	}
	delete(z.notCarrying, service)
	return "desync canary: " + why, true, true
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
// It returns the reason alongside the verdict so whatever files it says what was
// actually observed. The two stages fail for different reasons and want different
// next moves, and the single sentence this used to hand the brain — "connects but
// carries nothing" — was simply untrue of the first one.
func (c *Core) canaryProbe(ctx context.Context, service string) (bool, string) {
	svc, ok := c.reg.Services[service]
	if !ok || c.prober == nil {
		return false, "no prober for this service"
	}
	pos := 0
	for _, step := range svc.Chain {
		if step.StrategyClass == strategy.ClassZapret {
			pos = step.Position
			break
		}
	}
	if v := c.prober.Probe(ctx, service, pos); !v.OK {
		return false, "the recipe did not answer the probe at all: " + v.Err
	}
	ok, why, _ := c.goodputOK(ctx, svc)
	return ok, why
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
//
// measured separates "I pulled bytes and this is the answer" from every way of
// declining to pull them — disabled, no target, a live call, an endpoint smaller
// than the ask. Both used to return the same bare true, which is fine for a
// one-shot at apply time and wrong for anything periodic: a sweep that could not
// measure would otherwise clear a real verdict taken minutes earlier, and the
// escalation it was about to cause would vanish without a line saying so.
func (c *Core) goodputOK(ctx context.Context, svc registry.Service) (ok bool, why string, measured bool) {
	min := c.opts.CanaryGoodputKBps
	if min <= 0 {
		return true, "", false
	}
	// A dedicated bulk URL when the operator gave one; the probe target otherwise,
	// with the short-endpoint guard below to stop it producing a fake verdict.
	targets := volumeTargets(svc)
	if len(targets) == 0 {
		return true, "", false // nothing to pull volume from; the shallow verdict stands
	}
	target := strings.Join(targets, ", ")
	// Not while someone is playing. A burst probe pulls real bytes down the very
	// uplink it is measuring, so running it mid-session both spoils the session
	// and mismeasures the path. Skipping costs one uncredited recipe; not
	// skipping costs the household's game.
	if dataplane.RealtimeActive(ctx, c.clash) {
		c.log.Info("canary: live UDP session, not pulling volume", "service", svc.Name)
		return true, "", false
	}
	ok, why, measured, detail := c.measureVolume(ctx, svc,
		dataplane.BurstClient(c.opts.ProbeProxy, 15*time.Second),
		dataplane.H3Client(15*time.Second))
	if len(detail) > 0 {
		c.log.Info("canary: volume", "service", svc.Name, "target", target,
			"verdict", strings.Join(detail, "; "))
	}
	return ok, why, measured
}

// measureVolume pulls a rule's volume over every transport its targets name and
// takes the BEST outcome, while the reading within one transport is still the
// worst of its endpoints.
//
// The asymmetry is not a compromise, it is what the user experiences. Across
// DOMAINS worst-of is right: a service whose CDN is dead is dead, and judging it
// by its best domain is Goodhart. Across TRANSPORTS it is wrong: a browser tries
// QUIC, and when QUIC does not answer it falls back to TCP and the video plays.
// Condemning a recipe because the half the browser abandoned did not carry would
// hold YouTube on the tunnel forever on the strength of a path nobody uses when
// it is broken — the same shape as every other verdict-about-the-untouched in
// this codebase, just inverted.
//
// Both readings are returned in detail, so a QUIC path that quietly died is
// visible in the log even on a pass. That is the case worth catching: QUIC that
// is DROPPED costs a fallback delay, QUIC that is throttled is a video that
// stalls, and only a volume pull can tell them apart.
func (c *Core) measureVolume(ctx context.Context, svc registry.Service, tcp, h3 *http.Client) (bool, string, bool, []string) {
	ask := c.volumeBytes(svc)
	floor := c.volumeFloor(svc, ask)
	tcpEps, h3Eps := splitEndpoints(volumeTargets(svc), tcp, h3)

	var anyOK, anyMeasured bool
	var fails, detail []string
	for _, g := range []struct {
		name string
		eps  []burstprobe.Endpoint
	}{{"tcp", tcpEps}, {"quic", h3Eps}} {
		if len(g.eps) == 0 {
			continue
		}
		q, said := burstprobe.ProbeEndpointsSaying(ctx, g.eps, ask, volumeAttempts)
		ok, why, measured := volumeVerdict(q, ask, floor)
		// The transport's own sentence, carried into the verdict. "Nothing was
		// delivered" is where a SYN that got no answer and a swallowed ClientHello meet,
		// and which of the two it is decides whether a desync recipe is even the right
		// kind of tool for this rule.
		if !ok && measured && said != "" {
			why += " (" + said + ")"
		}
		anyOK = anyOK || ok
		anyMeasured = anyMeasured || measured
		switch {
		case ok:
			detail = append(detail, fmt.Sprintf("%s carried %d KiB at %.0f KiB/s", g.name, q.Bytes>>10, q.GoodputKBps))
		case !measured:
			detail = append(detail, g.name+" not judged: "+why)
		default:
			detail = append(detail, g.name+" "+why)
			fails = append(fails, g.name+" "+why)
		}
	}
	if anyOK || !anyMeasured {
		return anyOK, "", anyMeasured, detail
	}
	return false, strings.Join(fails, "; "), true, detail
}

// volumeAttempts is how many fresh pulls stand behind one verdict.
//
// It was 1, and one is not a measurement of a path that flaps. x delivered its
// full 32 KiB once and then timed out eight times running — 0 of 8 — and that one
// success was enough to pass both the canary and the lane, so every instrument
// called x healthy while the site would not load for the owner. Three fresh
// connections, and the verdict below requires ALL of them to carry: a recipe is
// moved onto live traffic on this evidence, and the operator must never be the
// one who finds out it only works sometimes.
const volumeAttempts = 3

// volumeVerdict turns one reading into a verdict. Shared by the live canary and
// the isolated lane so both mean the same thing by "this recipe carries" —
// they used to hold two copies of these rules and the copies had already drifted
// in wording, which is one edit away from drifting in substance.
//
// measured=false is "we could not ask", and it must never reach the knowledge
// base or a rung decision.
func volumeVerdict(q quality.Quality, ask int64, floor float64) (ok bool, why string, measured bool) {
	switch {
	case q.Samples == 0:
		return true, "the measurement did not happen", false
	case q.Short:
		// The endpoint ran out before we had pulled enough. That describes the URL,
		// not the path. youtube's probe target used to be `generate_204` — no body at
		// all — so its canary returned 0 KiB/s for every strategy ever applied and
		// demoted the lot; the KB ended with every zapret recipe for youtube at ewma 0
		// after ninety "failures" that measured nothing.
		return true, "the target has less to give than we ask for — set volume_bytes or a bigger volume_target", false
	case q.Bytes == 0 && q.Loss >= 1:
		// Nothing at all comes FIRST. It is not a freeze — a freeze is bytes that
		// flowed and then stopped — and with the order wrong a refused connection was
		// reported as "froze at 0 KiB".
		return false, "did not complete at all — no bytes, no response", true
	case q.Loss > 0:
		// Some attempts carried and some did not, and Bytes is the average over the
		// ones that DID — so a path that succeeds once in three reads as a full
		// delivery with a footnote. It is not: it is a path that fails most of the
		// time, and crowning it moves the household onto a recipe that works
		// occasionally.
		//
		// Measured on x: one pull delivered its full 32 KiB in 0.63s and the next
		// eight timed out at 15–18 KiB, 0 of 8. A single lucky attempt was enough to
		// pass both the canary and the lane, which is why every instrument called x
		// healthy while the site would not load. The threshold, not the instrument,
		// was the defect.
		return false, fmt.Sprintf("carried on %.0f%% of attempts — the path works only sometimes",
			(1-q.Loss)*100), true
	case q.Bytes < ask:
		return false, fmt.Sprintf("froze at %d KiB of the %d KiB asked", q.Bytes>>10, ask>>10), true
	case q.GoodputKBps < floor:
		return false, fmt.Sprintf("delivered all %d KiB but at %.0f KiB/s, under the %.0f floor",
			ask>>10, q.GoodputKBps, floor), true
	}
	return true, "", true
}

// volumeSweepInterval is how often a rule sitting on a desync rung is re-measured
// for volume. Slower than the probe (which is cheap and every Interval) because
// this one pulls real bytes down the operator's own uplink.
const volumeSweepInterval = 5 * time.Minute

// volumeLoop re-measures goodput for the rules currently on a desync rung.
//
// The gap it closes: judge() — the only thing that had ever pulled volume — runs
// from Enable, i.e. once, three seconds after a service ENTERS the rung. Reconcile
// deliberately takes no verdict. So in steady state, which is nearly all of the
// time, nothing measured volume at all, StallReason had nothing to report, and the
// probing engine read that silence as "not stalled". Measured on the ThinkPad on
// 2026-08-07: YouTube would not load in the browser for hours, the owner's own curl
// timed out three times out of three, and the journal held not one canary line —
// not because the branches were wrong (a handshake timeout lands squarely in "did
// not complete at all") but because the code that emits them was never called.
//
// A verdict is filed only when the measurement actually HAPPENED. A sweep that
// declined — a live call, an endpoint too small, the check disabled — must not
// clear a pending verdict from a real one, or the escalation it earned disappears.
func (c *Core) volumeLoop(ctx context.Context) {
	t := time.NewTicker(volumeSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sweepVolume(ctx)
		}
	}
}

// announceVolumeSweep says at startup which rules the sweep can actually judge.
//
// Without this the feature is invisible when it is inert, and inert is its normal
// state on a fresh config: `client/scaffold/recommended.yaml` ships no
// volume_target at all, so a reader sees a canary wired to the brain and concludes
// the volume dimension is live when nothing will ever measure it.
func (c *Core) announceVolumeSweep() {
	if c.opts.CanaryGoodputKBps <= 0 {
		c.log.Info("volume sweep off: -canary-goodput-kbps is 0, so nothing re-measures whether a desync recipe still carries traffic")
		return
	}
	var with, without []string
	for _, svc := range c.reg.Services {
		if !hasZapretRung(svc) {
			continue
		}
		if svc.VolumeTarget != "" {
			with = append(with, svc.Name)
		} else {
			without = append(without, svc.Name)
		}
	}
	sort.Strings(with)
	sort.Strings(without)
	// The same list governs the GATE: a rule with no volume_target cannot have a
	// candidate proven for it either, so its first recipe is applied unverified.
	// Saying which rules those are is the difference between a feature that is
	// partly armed and one that only looks armed.
	c.log.Info("volume sweep and candidate gate armed", "every", volumeSweepInterval,
		"judged", with, "unjudgeable_no_volume_target", without)
}

func hasZapretRung(svc registry.Service) bool {
	for _, step := range svc.Chain {
		if step.StrategyClass == strategy.ClassZapret {
			return true
		}
	}
	return false
}

// sweepVolume measures one pass. It only considers rules with an explicit
// volume_target: without one goodputOK falls back to the probe target, and probe
// targets are chosen to be TINY — that fallback is what taught the knowledge base
// that every recipe for youtube scored zero, by timing a 204 with no body.
func (c *Core) sweepVolume(ctx context.Context) {
	if c.zapExec == nil {
		return
	}
	for _, svc := range c.zapretServices() {
		if svc.VolumeTarget == "" {
			continue
		}
		ok, why, measured := c.goodputOK(ctx, svc)
		if !measured {
			continue
		}
		c.zapExec.noteCarrying(svc.Name, ok, why)
	}
}

// CarryingReason is the probing engine's activity oracle: is this rule visibly
// moving the user's own traffic right now? The judgement itself lives in
// pkg/observe beside the metrics it reads, because the router daemon needs the
// same answer and had none — it ran the stall oracle with nothing opposing it.
func (c *Core) CarryingReason(service string) (string, bool) {
	snap := c.observeSnapshot()
	m, ok := snap.Services[service]
	if !ok {
		return "", false
	}
	return c.carrying.Reason(service, m)
}

// volumeTargets is every bulk URL a rule declares, in one place so the canary,
// the sweep and the sandbox all judge a rule against the same set.
//
// Falling back to the PROBE target when no volume target is declared is
// deliberate but nearly always useless: probe targets are chosen to be tiny, and
// timing a 204 measures how small the URL is. The Short guard downstream catches
// that and refuses to form an opinion — which is the honest outcome, and the
// reason a rule without a volume_target simply cannot be judged for throughput.
func volumeTargets(svc registry.Service) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if (strings.HasPrefix(u, "http") || dataplane.IsH3(u)) && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	add(svc.VolumeTarget)
	for _, u := range svc.VolumeTargets {
		add(u)
	}
	if len(out) == 0 {
		add(svc.ProbeTarget)
	}
	return out
}

// volumeBytes is how much this rule asks for, its own figure when it has one.
//
// A rule whose endpoint is smaller than the ask cannot be judged at all: the read
// reaches end-of-body, the reading is marked Short, and the canary refuses to form
// an opinion — correctly, because the number would describe the URL. The way out
// is not a bigger URL but a smaller ask. It only has to clear the ~16 KiB where
// the freeze bites.
func (c *Core) volumeBytes(svc registry.Service) int64 {
	if svc.VolumeBytes > 0 {
		return int64(svc.VolumeBytes)
	}
	return c.opts.CanaryGoodputBytes
}

// volumeFloor is the throughput a rule demands, and it is DERIVED from the ask
// unless the rule says otherwise.
//
// The global default is 64 KiB/s, and it was chosen when the ask was 64 KiB — it
// means "deliver the whole thing in about a second". Applied unchanged to a
// smaller ask it silently gets harsher: against a 24 KiB target it demands the
// lot in 0.4s, which is a stiff bar for a path a censor is actively squeezing.
// Measured on the owner's laptop: three candidates for `x` delivered their full
// 24 KiB at 7, 21 and 36 KiB/s — the best of them plainly better than the
// incumbent, which freezes at 16 KiB and never finishes — and all three were
// rejected against a floor of 64.
//
// So the floor is the smaller of the global and one-ask-per-second. It never
// demands MORE than the operator configured, and it stops demanding a speed the
// ask is too small to ask for.
func (c *Core) volumeFloor(svc registry.Service, ask int64) float64 {
	if svc.VolumeFloorKBps > 0 {
		return svc.VolumeFloorKBps
	}
	perSecond := float64(ask) / 1024
	if g := c.opts.CanaryGoodputKBps; g > 0 && g < perSecond {
		return g
	}
	return perSecond
}

// splitEndpoints pairs each volume target with the transport it must be pulled
// over, keeping the two groups apart so each can be judged on its own.
//
// A target marked `h3://` goes over QUIC, which is the only way anything in this
// project has ever exercised a recipe's udp/443 profile. YouTube composes one,
// with a QUIC fake in it, and every probe and every volume pull until now went
// over TCP — so that profile rode into production on a measurement that could not
// touch it, on the service whose media is QUIC-first.
//
// A nil h3 client (the lane refuses one it cannot isolate) drops the h3 targets
// rather than quietly pulling them over TCP. A QUIC target measured over TCP is
// not a weaker measurement, it is a different one wearing the same name.
func splitEndpoints(targets []string, tcp, h3 *http.Client) (tcpEps, h3Eps []burstprobe.Endpoint) {
	for _, t := range targets {
		ep := burstprobe.Endpoint{URL: dataplane.FetchURL(t)}
		if dataplane.IsH3(t) {
			if h3 == nil {
				continue
			}
			ep.Client = h3
			h3Eps = append(h3Eps, ep)
			continue
		}
		if tcp == nil {
			continue
		}
		ep.Client = tcp
		tcpEps = append(tcpEps, ep)
	}
	return tcpEps, h3Eps
}
