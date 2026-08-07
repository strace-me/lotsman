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
func (c *Core) testRecipe(ctx context.Context, svc registry.Service, recipeID string) (bool, string) {
	sb, err := c.sandbox()
	if err != nil {
		return false, "no sandbox: " + err.Error()
	}
	client := dataplane.SandboxClient(zapret.TuneMark, c.wanIface, sandboxProbeTimeout)
	if client == nil {
		return false, "this platform cannot bind a probe to the interface, so a candidate cannot be measured without imposing it"
	}
	target := svc.VolumeTarget
	if target == "" {
		target = svc.ProbeTarget
	}
	if !strings.HasPrefix(target, "http") {
		return false, "rule has no http target to measure a candidate against"
	}

	// Compose the candidate for THIS rule alone, pinned to the recipe under test.
	// One service, so the argv is exactly the profiles that rule would get — not a
	// composition of the whole rung, which would measure everyone at once.
	plan := zaptune.ComposePinned([]registry.Service{svc}, c.zapExec.recipes, c.zapExec.pick,
		c.zapExec.resolve, c.opts.HostlistDir, zaptune.Pins{svc.Name: recipeID})
	if !plan.Covered || len(plan.Args) == 0 {
		return false, "candidate did not compose for this rule"
	}
	if got := plan.Chosen[svc.Name]; got != recipeID {
		// The pin was not honoured, so the composed argv is some OTHER recipe and a
		// verdict from it would be filed under this one. Principle 2, and the exact
		// failure a pinned strategy_id used to produce silently.
		return false, fmt.Sprintf("pin not honoured: composed %q, wanted %q", got, recipeID)
	}
	if _, err := writeHostlists(plan.Hostlists); err != nil {
		return false, "hostlist: " + err.Error()
	}

	c.sbMu.Lock()
	defer c.sbMu.Unlock()
	defer func() {
		if err := sb.Close(context.WithoutCancel(ctx)); err != nil {
			c.log.Warn("desync sandbox did not tear down cleanly", "err", err)
		}
	}()
	if err := sb.Apply(ctx, absolutizePayloads(plan.Args, c.opts.ZapretFiles)); err != nil {
		return false, "candidate did not start: " + err.Error()
	}

	// Pull real volume, not a status line: the failure this whole seam exists for
	// is a path that establishes and then freezes, and a header-only fetch scores
	// that as a win.
	want := c.opts.CanaryGoodputBytes
	q := burstprobe.Probe(ctx, client, []string{target}, want, 1)
	switch {
	case q.Samples == 0:
		return false, "the candidate measurement did not happen"
	case q.Short:
		// The endpoint ran out before we had pulled enough. That describes the URL,
		// not the path, so it cannot condemn the candidate — but it cannot crown it
		// either, and crowning it would move live traffic onto an unmeasured recipe.
		return false, "target too small to judge a candidate — set a volume_target for this rule"
	case q.Bytes == 0 && q.Loss >= 1:
		return false, "candidate carried nothing at all"
	case q.GoodputKBps < c.opts.CanaryGoodputKBps:
		return false, fmt.Sprintf("candidate carries only %.0f KiB/s (floor %.0f)", q.GoodputKBps, c.opts.CanaryGoodputKBps)
	}
	return true, fmt.Sprintf("%.0f KiB/s over %d KiB", q.GoodputKBps, q.Bytes>>10)
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
	c.rotMu.Lock()
	if c.rotAt == nil {
		c.rotAt = map[string]time.Time{}
	}
	if last, seen := c.rotAt[service]; seen && time.Since(last) < sandboxCooldown {
		c.rotMu.Unlock()
		return
	}
	c.rotAt[service] = time.Now()
	c.rotMu.Unlock()

	test := c.testCandidate
	if test == nil {
		test = c.testRecipe
	}
	for _, cand := range c.rankedCandidates(service, failed, sandboxCandidates) {
		if !c.zapExec.stillOnRung(service) {
			return // it escalated while we were measuring; the rung is not ours to steer
		}
		ok, why := test(ctx, svc, cand)
		// A sandbox verdict is a real measurement of that recipe on this network, so
		// it belongs in the knowledge base whichever way it went.
		c.kb.RecordOutcome(service, cand, ok, 0)
		// And again AFTER, because a measurement takes seconds and the brain moves on
		// its own tick: the rung was ours when we asked and may not be when we answer.
		// Applying then would change a rung the rule has left, for a reason nobody
		// asked for. Exactly the in-flight race the rung prober had.
		if !c.zapExec.stillOnRung(service) {
			c.log.Info("rule left the desync rung mid-measurement, not applying the candidate",
				"service", service, "candidate", cand)
			return
		}
		if !ok {
			c.log.Info("candidate rejected in the sandbox, real traffic untouched",
				"service", service, "candidate", cand, "why", why)
			continue
		}
		c.log.Info("candidate PASSED in the sandbox, moving the rule onto it",
			"service", service, "candidate", cand, "evidence", why, "replacing", failed)
		apply := c.applyForTest
		if apply == nil {
			apply = c.zapExec.Enable
		}
		if err := apply(ctx, service, cand); err != nil {
			c.log.Warn("candidate passed but could not be applied", "service", service, "candidate", cand, "err", err)
		}
		return
	}
	c.log.Info("no candidate passed the sandbox; leaving the rule to its chain",
		"service", service, "failed", failed)
}

// rankedCandidates lists recipes to try, best-scored first, excluding the one
// that just failed. It reuses the picker's own ranking so the sandbox tries what
// production would have tried, in the same order.
func (c *Core) rankedCandidates(service, exclude string, n int) []string {
	var out []string
	for _, r := range c.zapExec.recipes {
		if r.ID == exclude {
			continue
		}
		out = append(out, r.ID)
	}
	sortByScore(out, func(id string) float64 { return c.recipeScore(service, id) })
	if len(out) > n {
		out = out[:n]
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
