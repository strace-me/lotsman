package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/executor"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// productionResolver returns the resolver the sandbox lane should use so a probe
// target resolves to the SAME IP production dials — sing-box's DNS reached through
// the tun peer. Nil when there is no tun, where the system resolver is the only
// option (and the mismatch LOT-77 describes cannot be avoided). See
// dataplane.ProductionResolver.
func (c *Core) productionResolver() *net.Resolver {
	return dataplane.ProductionResolver(tunSentinel(c.tunOptions()), 4*time.Second)
}

func (c *Core) sandboxUTLSFingerprint() string {
	if c.conf == nil || c.conf.UTLSFingerprint == "" {
		return "chrome"
	}
	return c.conf.UTLSFingerprint
}

// resolveVolumeTarget resolves the rule's first volume target through the SAME
// resolver the lane dials with, so the log names the exact IP the measurement is
// against. Comparing it with the IP production actually dials is how a probe/prod
// divergence of the LOT-77 kind — same hostname, different CDN IP, a different
// bundle profile matched — becomes visible instead of hiding behind "the recipe
// failed". Returns "" when there is nothing to resolve; it never blocks the lane.
func (c *Core) resolveVolumeTarget(ctx context.Context, svc registry.Service) string {
	targets := volumeTargets(svc)
	if len(targets) == 0 {
		return ""
	}
	u, err := url.Parse(dataplane.FetchURL(targets[0]))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	r := c.productionResolver()
	if r == nil {
		r = net.DefaultResolver
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := r.LookupHost(lctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0]
}

// sandboxProbeTimeout bounds one candidate measurement end to end.
const sandboxProbeTimeout = 20 * time.Second

// sandboxCooldown is the minimum gap between two sandbox tests for the SAME
// rule. A test costs an engine start plus an nft table, so running one per probe
// tick would spend more on asking than on the answer.
const sandboxCooldown = 3 * time.Minute

// sandboxCandidates bounds how many recipes one rotation pass tries. The point
// is to move off a recipe that stopped working, not to search the catalogue;
// searching is the prospector's job and it has a budget for it.
const sandboxCandidates = 3

// testRecipe answers "would THIS recipe carry traffic for THIS rule, right now?"
// without letting any real traffic near it.
//
// This is the missing half of the loop the owner named: the canary should TEST,
// and only a success should move the rule's live traffic. Until now the only way
// to find out whether a recipe worked was to put it into production and watch —
// so every wrong guess cost the user a broken service, and a rule that guessed
// wrong once escalated to VPN and never came back to try a second.
//
// How it stays out of production's way: the sandbox owns a second nft table and a
// second queue, and only packets carrying TuneMark are diverted to it. The
// production table returns marked packets untouched (GenerateNft emits that rule
// first), so the probe is desynced exactly once — by the candidate — and the
// household's traffic never meets it. The probe socket also binds to the physical
// interface, because under our own tun an unbound socket is pulled into the
// tunnel and would measure the VPN while wearing the candidate's name.
// measured separates "the lane ran and this is the answer" from every way of
// failing to ask — no sandbox, wrong platform, nothing to fetch, a candidate that
// would not compose. The distinction is load-bearing twice over: an unmeasured
// verdict must not reach the knowledge base (principle 2), and the gate in front
// of a FIRST application must fail OPEN on it. A sandbox that is merely broken
// would otherwise silently pin every rule to the tunnel forever, which is a much
// worse failure than the one the gate prevents.
func (c *Core) testRecipe(ctx context.Context, svc registry.Service, recipeID string) (ok, measured bool, why string) {
	// The lane runs for seconds and a Reload rebuilds the executor underneath it,
	// so every read of c.zapExec on this path is a race with a teardown. Refuse
	// with a reason rather than measure against a half-torn loop: an unmeasurable
	// pass must say it did not measure (measured=false), never that the recipe
	// failed — that is principle 2, and the crash of 2026-08-13 came from the same
	// field being read on this path without a guard.
	if c.zapExec == nil {
		return false, false, "the desync executor was rebuilt mid-pass — nothing was measured"
	}
	// BEFORE the lane goes up, not after. Whether this rule can be judged at all is
	// knowable from its config, and asking afterwards cost an nft table and an nfqws
	// start per question: measured on the laptop as 104 sandbox lifts in 2m23s, one
	// every 1.4 seconds, ninety of which refused for exactly this reason. Zero
	// information, real work, on a machine running off a battery.
	if !c.canJudge(svc) {
		return false, false, "rule has no volume_target, so the lane has nothing to judge a candidate by"
	}
	// A cancelled context is not a verdict about the recipe. Without this check the
	// first thing to notice was `nft`, which reported `context canceled` from inside
	// the install — an error about the recipe's own plumbing, filed against the
	// recipe. Measured as `x` reporting "no candidate passed the sandbox" forever on
	// a network where the lane had simply never come up.
	if err := ctx.Err(); err != nil {
		return false, false, "the loop was torn down before this candidate could be measured: " + err.Error()
	}
	sb, err := c.sandbox()
	if err != nil {
		return false, false, "no sandbox: " + err.Error()
	}
	client := dataplane.SandboxClient(zapret.TuneMark, c.wanIface, sandboxProbeTimeout, c.productionResolver(), c.sandboxUTLSFingerprint())
	if client == nil {
		return false, false, "this platform cannot bind a probe to the interface, so a candidate cannot be measured without imposing it"
	}
	// The QUIC half of the lane. It carries the same mark and the same binding, so
	// a `h3://` target measures the candidate's udp/443 profile through the sandbox
	// queue — the profile nothing in this project had ever measured.
	h3 := dataplane.SandboxH3Client(zapret.TuneMark, c.wanIface, sandboxProbeTimeout, c.productionResolver())

	// In preset mode there is nothing to compose and nothing to choose: the lane
	// measures the bundle the operator asked for, exactly as production will run it.
	// Composing here instead would test a per-rule rendering of a whole-machine
	// preset — a different strategy under the same name, which is the failure the
	// mode exists to avoid.
	if len(c.zapExec.presets) > 0 {
		p, ok := c.zapExec.presetByName(recipeID)
		if !ok {
			return false, false, "no preset by that name is declared: " + recipeID
		}
		return c.measureArm(ctx, svc, sb, client, h3, p.Name, absolutizePayloads(p.Args, c.opts.ZapretFiles))
	}
	// Compose the candidate for THIS rule alone, pinned to the recipe under test.
	// One service, so the argv is exactly the profiles that rule would get — not a
	// composition of the whole rung, which would measure everyone at once.
	plan := zaptune.ComposePinned([]registry.Service{svc}, c.zapExec.recipes, c.zapExec.pick,
		c.zapExec.resolve, c.opts.HostlistDir, zaptune.Pins{svc.Name: recipeID})
	if !plan.Covered || len(plan.Args) == 0 {
		return false, false, "candidate did not compose for this rule"
	}
	if got := plan.Chosen[svc.Name]; got != recipeID {
		// The pin was not honoured, so the composed argv is some OTHER recipe and a
		// verdict from it would be filed under this one. Principle 2, and the exact
		// failure a pinned strategy_id used to produce silently.
		return false, false, fmt.Sprintf("pin not honoured: composed %q, wanted %q", got, recipeID)
	}
	if _, err := writeHostlists(plan.Hostlists); err != nil {
		return false, false, "hostlist: " + err.Error()
	}

	argv := absolutizePayloads(plan.Args, c.opts.ZapretFiles)
	return c.measureArm(ctx, svc, sb, client, h3, recipeID, argv)
}

// testBaseline measures the same path through the same lane with NO desync on the
// queue — the control arm.
//
// Without it a lane verdict is uninterpretable in exactly the case that matters.
// Twenty-two candidates for youtube came back with the identical sentence, "did
// not complete at all — no bytes, no response", and from outside that has two
// completely different causes: the censor kills the handshake no matter what we
// send, or our profile never touched the traffic and the probe went out bare. The
// owning session read the first and said honestly that it could not tell them
// apart. Neither could the instrument.
//
// The sandbox has always supported this — an empty argv leaves the table up and
// nothing on the queue, so `flags bypass` passes the marked probe through
// untouched — and nothing ever asked it for one. A candidate that scores exactly
// what the baseline scores changed nothing; a baseline that PASSES says the path
// was never broken here and the rule wants no recipe at all.
func (c *Core) testBaseline(ctx context.Context, svc registry.Service) (ok, measured bool, why string) {
	if !c.canJudge(svc) {
		return false, false, "rule has no volume_target, so the lane has nothing to judge a baseline by"
	}
	if err := ctx.Err(); err != nil {
		return false, false, "the loop was torn down before the baseline could be measured: " + err.Error()
	}
	sb, err := c.sandbox()
	if err != nil {
		return false, false, "no sandbox: " + err.Error()
	}
	client := dataplane.SandboxClient(zapret.TuneMark, c.wanIface, sandboxProbeTimeout, c.productionResolver(), c.sandboxUTLSFingerprint())
	if client == nil {
		return false, false, "this platform cannot bind a probe to the interface"
	}
	h3 := dataplane.SandboxH3Client(zapret.TuneMark, c.wanIface, sandboxProbeTimeout, c.productionResolver())
	return c.measureArm(ctx, svc, sb, client, h3, "no-desync control", nil)
}

// measureArm lifts the lane with argv (nil = the no-desync baseline), pulls the
// rule's volume through it and tears it down. The one place a lane measurement
// happens, so the candidate and its control are measured by identical machinery —
// a control that differed from the thing it controls would answer a question
// nobody asked.
func (c *Core) measureArm(ctx context.Context, svc registry.Service, sb *zapret.Sandbox,
	client, h3 *http.Client, label string, argv []string) (ok, measured bool, why string) {
	c.sbMu.Lock()
	defer c.sbMu.Unlock()
	defer func() {
		if err := sb.Close(context.WithoutCancel(ctx)); err != nil {
			c.log.Warn("desync sandbox did not tear down cleanly", "err", err)
		}
	}()
	// The LANE's own context, not the caller's. Installing an nft table and starting
	// an engine are two steps that must both happen or neither: cancelled between
	// them, the table stays up with nothing on its queue. Bounded so a wedged `nft`
	// cannot hold the lane's lock forever.
	setup, cancelSetup := context.WithTimeout(context.WithoutCancel(ctx), sandboxProbeTimeout)
	defer cancelSetup()
	// The FULL argv, recorded HERE — holding the lane, one line before the apply it
	// describes. Logged at composition time instead, it sat outside the lock, and
	// another rule's lift landed between the two: the record then showed a
	// candidate's argv followed by somebody else's "candidate applied", three
	// seconds and one service apart. The measurement was correct and serialized;
	// only the record was misleading, which is the more dangerous of the two,
	// because it is what a human reads when the numbers stop making sense.
	//
	// Production records its own argv on every exit; without the same record here
	// the two cannot be diffed, and the first time they disagreed the only available
	// comparison was against a log line that omits hostlists by design.
	if len(argv) > 0 {
		c.log.Info("sandbox candidate argv", "service", svc.Name, "candidate", label,
			"argv", strings.Join(argv, " "))
	} else {
		c.log.Info("sandbox control arm: nothing on the queue", "service", svc.Name)
	}
	// Name the IP the lane measured. A probe/prod divergence (LOT-77) is invisible
	// without it: "the recipe failed" reads the same whether the recipe is wrong or
	// the probe dialled a CDN IP production never uses.
	if ip := c.resolveVolumeTarget(ctx, svc); ip != "" {
		c.log.Info("sandbox lane target", "service", svc.Name, "candidate", label, "ip", ip)
	}
	if err := sb.Apply(setup, argv); err != nil {
		// nfqws validates its inputs after dropping privileges, so a refusal here is
		// a real verdict ABOUT THIS RECIPE — it cannot run on this host — even though
		// it says nothing about the network. Measured, and worth remembering.
		return false, true, "candidate did not start: " + err.Error()
	}

	// Pull real volume, not a status line: the failure this whole seam exists for
	// is a path that establishes and then freezes, and a header-only fetch scores
	// that as a win. The same judgement the live canary uses, over the same
	// transports — a lane that scored a candidate by different rules than the canary
	// that later judges it in production would hand the rule between two graders who
	// disagree.
	ok, why, measured, detail := c.measureVolume(ctx, svc, client, h3)
	if len(detail) > 0 {
		why = strings.Join(detail, "; ")
	}
	return ok, measured, why
}

// sandbox builds the isolated test lane on first use and reuses it after. It
// mirrors production's capture and connbytes so a candidate is measured through
// the same shape of queue it would run behind.
func (c *Core) sandbox() (*zapret.Sandbox, error) {
	c.sbInitMu.Lock()
	defer c.sbInitMu.Unlock()
	if c.sb != nil {
		return c.sb, nil
	}
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("desync sandbox is Linux-only (nft + NFQUEUE)")
	}
	wan := c.wanIface()
	if wan == "" {
		return nil, fmt.Errorf("egress interface unknown, cannot scope the sandbox table")
	}
	prod := c.qnum()
	c.sb = &zapret.Sandbox{
		Opts: zapret.IsolateOptions{
			Table: "inet lotsman_probe",
			WAN:   wan,
			// One above production's. Sandbox refuses to share it, and sharing would
			// hand real traffic to a candidate nobody has measured.
			QNum:  prod + 1,
			TCP:   []string{"80", "443", "2053", "2083", "2087", "2096", "8443"},
			UDP:   []string{"443"},
			Bytes: defaultConnbytes,
			// Before production's mangle hook, so the marked probe is claimed here and
			// never reaches the production queue.
			Prio: -200,
		},
		Bin: c.opts.NfqwsBin, Dir: c.opts.ZapretFiles,
		Run: executor.ExecRunner{},
		// Hear the engine even when it lives. A candidate that starts, complains
		// about an argument this build does not know and runs without it is measured
		// as the full recipe and scored under its name.
		Launch: zapret.ExecLauncherSaying(0, func(said string) {
			c.log.Warn("the desync engine said something while starting a candidate", "said", said)
		}),
		// Not the value read on the line above: this Sandbox outlives a roam, and
		// the table names the interface every time it is installed.
		WAN:      c.wanIface,
		ProdQNum: prod, Log: c.log,
	}
	return c.sb, nil
}

// closeSandbox tears the test lane down for good. Called on shutdown: a leftover
// table quietly diverting marked packets, or an engine nobody tracks, would make
// the NEXT run measure through the wreckage.
func (c *Core) closeSandbox(ctx context.Context) {
	c.sbInitMu.Lock()
	sb := c.sb
	c.sb = nil
	c.sbInitMu.Unlock()
	if sb == nil {
		return
	}
	c.sbMu.Lock()
	defer c.sbMu.Unlock()
	if err := sb.Close(ctx); err != nil {
		c.log.Warn("desync sandbox teardown", "err", err)
	}
}

// rotateRecipe is the canary's second move. The recipe in production stopped
// carrying, so try the next candidates IN THE SANDBOX and move the rule's real
// traffic only onto one that passed.
//
// Before this, a failing recipe only lost KB score, and the score only mattered
// the next time the rule ENTERED the rung — which, once automatic recovery from
// VPN was removed as dishonest, was never. So a rule tried exactly one recipe per
// session: guess wrong, escalate, stay on the tunnel forever. Measured on the
// owner's laptop as all eleven rules on VPN within an hour.
func (c *Core) rotateRecipe(ctx context.Context, service, failed string) {
	svc, ok := c.reg.Services[service]
	if !ok || c.zapExec == nil {
		return
	}
	if !c.claimRotation(service) {
		return
	}
	cand, verified := c.provenCandidate(ctx, svc, failed, false)
	if cand == "" {
		c.log.Info("no candidate passed the sandbox; leaving the rule to its chain",
			"service", service, "failed", failed)
		return
	}
	c.applyCandidate(ctx, service, cand, verified, failed)
}

// gateEnable is the same gate in front of a FIRST application: the rule is
// arriving on the desync rung, so prove a recipe in the lane before its traffic
// is routed there at all.
//
// Asynchronous on purpose. Enable sits on the applier's path and a measurement
// takes seconds; blocking there would stall every other rule's convergence. So
// the rule keeps whatever routing it already had — usually the tunnel, which
// works — until something is proven, instead of being dropped onto an unproven
// recipe and taken off it again three probes later.
//
// It fails OPEN. If the lane could not measure at all — no sandbox, wrong
// platform, nothing to fetch — the top candidate is applied unverified, exactly
// as before this existed. A gate that failed closed on its own breakage would
// pin every rule to the tunnel and call it caution.
func (c *Core) gateEnable(ctx context.Context, service string) {
	svc, ok := c.reg.Services[service]
	if !ok || c.zapExec == nil {
		return
	}
	// The applier RE-ASSERTS on every interval, so Enable arrives again and again
	// for a rule that has not moved. Without this the whole pass re-ran each time —
	// x proposed the same three recipes four times in two minutes. The cooldown must
	// not block the APPLICATION though, only the re-test: a gate that skipped both
	// would leave the rule with no route at all.
	if !c.canJudge(svc) || !c.claimRotation(service) {
		if top := c.rankedCandidates(service, "", 1); len(top) > 0 {
			c.applyCandidate(ctx, service, top[0], false, "")
		}
		return
	}
	cand, verified := c.provenCandidate(ctx, svc, "", true)
	if cand == "" {
		c.log.Info("no recipe could be proven for this rule; not routing its traffic to the desync rung",
			"service", service)
		return
	}
	c.applyCandidate(ctx, service, cand, verified, "")
}

// canJudge reports whether the lane could form an opinion about this rule at all.
// It needs a target with real VOLUME behind it: a probe target is deliberately
// tiny — youtube's is a 204 with no body — and timing one measures how small the
// URL is, which is exactly how the knowledge base once learned that every recipe
// scored zero.
func (c *Core) canJudge(svc registry.Service) bool {
	return strings.HasPrefix(svc.VolumeTarget, "http") || dataplane.IsH3(svc.VolumeTarget) ||
		len(svc.VolumeTargets) > 0
}

// claimRotation enforces the per-rule cooldown. Each test costs an engine start
// and an nft table, so a rule that keeps failing must not spend the machine on
// asking the same question.
func (c *Core) claimRotation(service string) bool {
	c.rotMu.Lock()
	defer c.rotMu.Unlock()
	if c.rotAt == nil {
		c.rotAt = map[string]time.Time{}
	}
	if last, seen := c.rotAt[service]; seen && time.Since(last) < sandboxCooldown {
		return false
	}
	c.rotAt[service] = time.Now()
	return true
}

// provenCandidate returns the first candidate that PASSED in the sandbox, and
// whether that answer came from an actual measurement. With failOpen, a lane that
// could not measure at all yields the top candidate marked unverified rather than
// nothing at all.
func (c *Core) provenCandidate(ctx context.Context, svc registry.Service, exclude string, failOpen bool) (string, bool) {
	cand, verified, _ := c.provenCandidateFor(ctx, svc, exclude, failOpen, true)
	return cand, verified
}

// provenCandidateFor is provenCandidate with the rung check made optional, and
// with the pass's own honesty reported back: measured says whether ANY candidate
// was actually put in front of the censor.
//
// The third return value is not decoration. Without it "no candidate passed" and
// "the lane never asked" are the same value, and a caller deciding a rule's fate
// on it manufactures a failure out of its own inability to measure — principle 2,
// and it was live on the owner's laptop: discord has no volume_target, so all
// three of its candidates refused before the lane even lifted, and the recovery
// probe reported that as a hard NO every ten seconds. A failed silent probe
// RESETS the brain's recovery counter, so the rule the owner wants on desync was
// being held on the tunnel by a measurement that never happened.
//
// steering means "we intend to change what this rule is running", and then the
// rule must still be on the rung we are steering. Recovery is the other case: the
// rule is deliberately somewhere else and we are asking whether it COULD come
// back, so requiring it to be here already would refuse the only question worth
// asking.
func (c *Core) provenCandidateFor(ctx context.Context, svc registry.Service, exclude string, failOpen, steering bool) (cand string, verified, measured bool) {
	// Take the executor ONCE. A pass runs for seconds, and a Reload tears the loop
	// down and rebuilds it — core.go sets c.zap and c.zapExec to nil in between —
	// so re-reading the field mid-pass is a race with a teardown, not a lookup. It
	// crashed the client on 2026-08-13: the post-measurement re-check below
	// dereferenced c.zapExec after a rebuild, and a nil-pointer panic in a gate
	// goroutine takes the whole process, which systemd then left down for eight
	// minutes. Two guards existed on this path already and this third read had
	// none, precisely because it happens LATEST — when a teardown is most likely to
	// have run.
	zx := c.zapExec
	if zx == nil {
		return "", false, false
	}
	test := c.testCandidate
	if test == nil {
		test = c.testRecipe
	}
	cands := c.rankedCandidates(svc.Name, exclude, sandboxCandidates)
	if len(cands) == 0 {
		return "", false, false
	}
	// The control arm, once per pass. What it costs is one extra lift; what it buys
	// is the difference between "every recipe lost" and "nothing we did reached the
	// traffic", which are indistinguishable from the verdict alone and were being
	// read as the first.
	base := c.baseline(ctx, svc)
	anyMeasured := false
	for _, cand := range cands {
		if steering && !zx.stillOnRung(svc.Name) {
			return "", false, anyMeasured // the brain moved it; the rung is not ours to steer
		}
		ok, measured, why := test(ctx, svc, cand)
		anyMeasured = anyMeasured || measured
		// Only a real measurement teaches the knowledge base. Recording "could not
		// ask" as a failure would demote a recipe for the sandbox's own breakage —
		// a verdict about something the measurement did not touch.
		if measured {
			c.kb.RecordOutcome(svc.Name, cand, ok, 0)
		}
		// Re-checked AFTER too: a measurement takes seconds and the brain moves on
		// its own tick, so the rung was ours when we asked and may not be when we
		// answer. Exactly the in-flight race the rung prober had.
		if steering && !zx.stillOnRung(svc.Name) {
			c.log.Info("rule left the desync rung mid-measurement, not applying the candidate",
				"service", svc.Name, "candidate", cand)
			return "", false, anyMeasured
		}
		if ok {
			// measured=true on the WINNING line too. A reader counting measured=true to
			// find real measurements got zero, because only refusals carried the field.
			c.log.Info("candidate PASSED in the sandbox",
				"service", svc.Name, "candidate", cand, "measured", true, "evidence", why)
			return cand, true, true
		}
		c.log.Info("candidate rejected in the sandbox, real traffic untouched",
			"service", svc.Name, "candidate", cand, "measured", measured, "why", why,
			"vs_no_desync", base)
	}
	// Falling open is decided over the WHOLE pass, not on the first candidate that
	// could not be measured. Deciding per-candidate meant one unmeasurable recipe
	// short-circuited the rest and got applied unverified, so a lane that worked
	// perfectly well for the second candidate was never asked.
	// Every candidate lost. Whether that is a statement about the recipes or about
	// the lane is exactly what the control answers, and it must be said out loud
	// rather than left for a reader to infer from two log lines an hour apart.
	if anyMeasured {
		c.log.Info("no candidate beat the control in the lane",
			"service", svc.Name, "tried", len(cands), "no_desync", base)
	}
	if failOpen && !anyMeasured {
		c.log.Warn("the test lane could not measure anything for this rule; applying the top candidate unverified rather than leaving the rule unrouted",
			"service", svc.Name, "candidate", cands[0])
		return cands[0], false, false
	}
	return "", false, anyMeasured
}

// baseline measures the rule's path through the lane with nothing on the queue and
// renders it as one short phrase for the log.
//
// Cached for the same window as a lane verdict, because the control does not need
// re-taking between the three candidates of one pass and a lift is not free.
func (c *Core) baseline(ctx context.Context, svc registry.Service) string {
	c.baseMu.Lock()
	if c.baseAt == nil {
		c.baseAt = map[string]baselineArm{}
	}
	if b, seen := c.baseAt[svc.Name]; seen && time.Since(b.at) < sandboxCooldown {
		c.baseMu.Unlock()
		return b.why
	}
	c.baseMu.Unlock()

	test := c.testBaselineFn
	if test == nil {
		test = c.testBaseline
	}
	ok, measured, why := test(ctx, svc)
	switch {
	case !measured:
		why = "not measured: " + why
	case ok:
		// The path carries with NO desync at all. Then the rule is not blocked here and
		// no recipe is what it needs — and a candidate "passing" would be taking credit
		// for a path that was never broken.
		why = "CARRIES WITHOUT DESYNC — " + why
		c.log.Warn("the lane carried this rule's volume with no desync on the queue at all",
			"service", svc.Name, "evidence", why)
	}
	c.baseMu.Lock()
	c.baseAt[svc.Name] = baselineArm{at: time.Now(), why: why}
	c.baseMu.Unlock()
	return why
}

// applyCandidate is the only place a recipe becomes the thing in service.
func (c *Core) applyCandidate(ctx context.Context, service, cand string, verified bool, replacing string) {
	c.log.Info("moving the rule onto a recipe",
		"service", service, "recipe", cand, "verified", verified, "replacing", replacing)
	apply := c.applyForTest
	if apply == nil {
		// Same teardown race as provenCandidateFor: this runs at the END of a pass
		// that took seconds, and a Reload may have rebuilt the loop meanwhile. Saying
		// so rather than returning silently — a candidate that was proven and then not
		// applied is exactly the kind of thing that must not vanish from the record.
		if c.zapExec == nil {
			c.log.Warn("desync executor was rebuilt mid-pass; the proven candidate is not applied",
				"service", service, "recipe", cand)
			return
		}
		apply = c.zapExec.applyNow
	}
	if err := apply(ctx, service, cand); err != nil {
		c.log.Warn("recipe could not be applied", "service", service, "recipe", cand, "err", err)
	}
}

// rankedCandidates lists recipes to try, best-scored first, excluding the one
// that just failed. It reuses the picker's own ranking so the sandbox tries what
// production would have tried, in the same order.
func (c *Core) rankedCandidates(service, exclude string, n int) []string {
	svc, ok := c.reg.Services[service]
	if !ok || c.zapExec == nil {
		return nil // no executor: an empty list is already how "nothing to try" is said here
	}
	// One bundle serves every rule, so there is exactly one thing to try and
	// nothing to rank. Offering the catalogue here would let the lane crown a
	// per-rule recipe that production, running the preset, is never going to apply.
	if len(c.zapExec.presets) > 0 {
		// The bundles ARE the candidates, ranked by what has carried before, exactly as
		// recipes are. This is the owner's ask made mechanical: the prober tries whole
		// strategies instead of fragments of one.
		var names []string
		for _, p := range c.zapExec.presets {
			if p.Name != exclude {
				names = append(names, p.Name)
			}
		}
		sortByScore(names, func(id string) float64 { return c.recipeScore(service, id) })
		if len(names) > n {
			names = names[:n]
		}
		return names
	}
	var out []string
	// Only recipes the COMPOSER will honour for this rule. Offering anything else
	// gets the picker's choice back instead of the candidate, so the test measures
	// the incumbent while wearing the candidate's name — and the gate, seeing the
	// mismatch, refuses it. Measured on hardware the day the lane first ran: every
	// one of sixteen passes ended "pin not honoured", zero candidates ever reached
	// the queue, and the gate looked like it was working because it refused
	// everything.
	for _, r := range zaptune.UsableCandidates(svc, c.zapExec.recipes) {
		if r.ID == exclude {
			continue
		}
		out = append(out, r.ID)
	}
	sortByScore(out, func(id string) float64 { return c.recipeScore(service, id) })
	// The operator's own pin goes FIRST, ahead of anything the knowledge base
	// prefers — but only if the composer would honour it. A pin is an instruction,
	// and testing it third would mean an operator who pinned a strategy watched
	// something else get applied, which is the silent substitution the pin
	// mechanism exists to end.
	if pin := c.zapExec.pinFor(service); pin != "" && pin != exclude && listed(out, pin) {
		out = append([]string{pin}, without(out, pin)...)
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func listed(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func without(ids []string, drop string) []string {
	out := ids[:0]
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

func sortByScore(ids []string, score func(string) float64) {
	// Insertion sort: the list is short and this keeps catalogue order as the
	// tie-break, which is what the picker does when the KB has nothing to say.
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && score(ids[j]) > score(ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

// baselineArm is one control measurement and when it was taken.
type baselineArm struct {
	at  time.Time
	why string
}
