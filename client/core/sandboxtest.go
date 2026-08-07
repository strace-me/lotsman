package core

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/burstprobe"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/executor"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

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
	sb, err := c.sandbox()
	if err != nil {
		return false, false, "no sandbox: " + err.Error()
	}
	client := dataplane.SandboxClient(zapret.TuneMark, c.wanIface, sandboxProbeTimeout)
	if client == nil {
		return false, false, "this platform cannot bind a probe to the interface, so a candidate cannot be measured without imposing it"
	}
	target := svc.VolumeTarget
	if target == "" {
		target = svc.ProbeTarget
	}
	if !strings.HasPrefix(target, "http") {
		return false, false, "rule has no http target to measure a candidate against"
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

	c.sbMu.Lock()
	defer c.sbMu.Unlock()
	defer func() {
		if err := sb.Close(context.WithoutCancel(ctx)); err != nil {
			c.log.Warn("desync sandbox did not tear down cleanly", "err", err)
		}
	}()
	if err := sb.Apply(ctx, absolutizePayloads(plan.Args, c.opts.ZapretFiles)); err != nil {
		// nfqws validates its inputs after dropping privileges, so a refusal here is
		// a real verdict ABOUT THIS RECIPE — it cannot run on this host — even though
		// it says nothing about the network. Measured, and worth remembering.
		return false, true, "candidate did not start: " + err.Error()
	}

	// Pull real volume, not a status line: the failure this whole seam exists for
	// is a path that establishes and then freezes, and a header-only fetch scores
	// that as a win.
	want := c.opts.CanaryGoodputBytes
	q := burstprobe.Probe(ctx, client, []string{target}, want, 1)
	switch {
	case q.Samples == 0:
		return false, false, "the candidate measurement did not happen"
	case q.Short:
		// The endpoint ran out before we had pulled enough. That describes the URL,
		// not the path, so it cannot condemn the candidate — but it cannot crown it
		// either, and crowning it would move live traffic onto an unmeasured recipe.
		return false, false, "target too small to judge a candidate — set a volume_target for this rule"
	case q.Bytes == 0 && q.Loss >= 1:
		return false, true, "candidate carried nothing at all"
	case q.GoodputKBps < c.opts.CanaryGoodputKBps:
		return false, true, fmt.Sprintf("candidate carries only %.0f KiB/s (floor %.0f)", q.GoodputKBps, c.opts.CanaryGoodputKBps)
	}
	return true, true, fmt.Sprintf("%.0f KiB/s over %d KiB", q.GoodputKBps, q.Bytes>>10)
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
		Run: executor.ExecRunner{}, Launch: zapret.ExecLauncher(0),
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
	cand, verified := c.provenCandidate(ctx, svc, "", true)
	if cand == "" {
		c.log.Info("no recipe could be proven for this rule; not routing its traffic to the desync rung",
			"service", service)
		return
	}
	c.applyCandidate(ctx, service, cand, verified, "")
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
	test := c.testCandidate
	if test == nil {
		test = c.testRecipe
	}
	cands := c.rankedCandidates(svc.Name, exclude, sandboxCandidates)
	if len(cands) == 0 {
		return "", false
	}
	anyMeasured := false
	for _, cand := range cands {
		if !c.zapExec.stillOnRung(svc.Name) {
			return "", false // the brain moved it; the rung is not ours to steer
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
		if !c.zapExec.stillOnRung(svc.Name) {
			c.log.Info("rule left the desync rung mid-measurement, not applying the candidate",
				"service", svc.Name, "candidate", cand)
			return "", false
		}
		if ok {
			c.log.Info("candidate PASSED in the sandbox", "service", svc.Name, "candidate", cand, "evidence", why)
			return cand, true
		}
		c.log.Info("candidate rejected in the sandbox, real traffic untouched",
			"service", svc.Name, "candidate", cand, "measured", measured, "why", why)
	}
	// Falling open is decided over the WHOLE pass, not on the first candidate that
	// could not be measured. Deciding per-candidate meant one unmeasurable recipe
	// short-circuited the rest and got applied unverified, so a lane that worked
	// perfectly well for the second candidate was never asked.
	if failOpen && !anyMeasured {
		c.log.Warn("the test lane could not measure anything for this rule; applying the top candidate unverified rather than leaving the rule unrouted",
			"service", svc.Name, "candidate", cands[0])
		return cands[0], false
	}
	return "", false
}

// applyCandidate is the only place a recipe becomes the thing in service.
func (c *Core) applyCandidate(ctx context.Context, service, cand string, verified bool, replacing string) {
	c.log.Info("moving the rule onto a recipe",
		"service", service, "recipe", cand, "verified", verified, "replacing", replacing)
	apply := c.applyForTest
	if apply == nil {
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
	if !ok {
		return nil
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
