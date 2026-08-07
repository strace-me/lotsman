package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/strace-me/lotsman/pkg/aggregate"

	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// zapretExec is the client's zapret-class StrategyExecutor. It keeps the router's
// contract — route the service DIRECT so its traffic reaches the local desync
// engine instead of disappearing into the tunnel — but instead of symlinking a
// launcher and restarting an init service, it recomposes the whole nfqws strategy
// and hands the argv to a managed nfqws process.
//
// Recomposition is WHOLE-config, never incremental: nfqws is one process carrying
// every service's --new block, and the StrategyExecutor interface only reports a
// service ENTERING a rung — never leaving it. Composing from remembered Enable
// calls would therefore keep desyncing services that moved to VPN long ago.
// desyncEngine is the slice of the nfqws engine the executor drives. It is an
// interface purely so the recompose/stop logic can be tested without a live
// NFQUEUE; *nfqws.Engine is the only implementation.
type desyncEngine interface {
	Apply(ctx context.Context, args []string) (restarted bool, err error)
	Stop(ctx context.Context) error
}

// desyncPlatformEngine is the engine as the CORE holds it: the executor's slice
// above, plus the measured liveness the status surface reads. It is an interface
// because there are now two implementations — nfqws behind an nft NFQUEUE on
// Linux, winws behind its own WinDivert filter on Windows — and the choice
// between them is made in exactly one place (Core.newZapretExec).
type desyncPlatformEngine interface {
	desyncEngine
	Alive() bool
}

type zapretExec struct {
	clash     *dataplane.ClashClient
	engine    desyncEngine
	recipes   []strategycat.Recipe
	pick      zaptune.Picker            // ranks candidate recipes (KB-learned, catalog order as tiebreak)
	files     string                    // zapret payload dir, for resolving .bin references
	hostlists string                    // dir for per-service hostlist files ("" = inline the domains)
	active    func() []registry.Service // services CURRENTLY on a zapret rung
	resolve   zaptune.Resolver
	// canary probes the service just after a strategy lands, and record folds that
	// verdict into the KB. Without them the picker cannot learn which recipe beats
	// the DPI in front of THIS machine.
	// life is the client's own lifetime. The verdict must not ride the applier's
	// per-call context (cancelled the moment Enable returns) nor escape
	// cancellation entirely — it has to die when the data plane does.
	life func() context.Context
	// canary returns the verdict and WHY it failed, in the words of the stage that
	// observed it. The reason used to be composed by the reader instead, which made
	// it assert "the path connects but carries nothing" over a canary that had
	// failed at the shallow probe — i.e. over a path that never connected at all.
	canary func(ctx context.Context, service string) (ok bool, why string)
	// notCarrying is the canary's standing verdict per service — the reason string,
	// present only while a verdict is pending — read by the probing engine through
	// StallReason. Guarded separately from the executor's own lock: the probe engine
	// asks on its goroutine while a judge writes on another.
	carryMu     sync.Mutex
	notCarrying map[string]string
	// pinned is the brain's resolved strategy per service — a chain step's
	// strategy_id. Guarded by z.mu, which Enable and the recompose both hold.
	pinned map[string]string
	// warnedPins remembers which unusable pin was already reported per service, so
	// a standing condition is announced once instead of on every recompose.
	warnedPins map[string]string
	record     func(service, recipe string, ok bool)
	// rotate is the canary's second move: the recipe in production stopped
	// carrying, so try the next candidates in the isolated test lane and switch
	// production only onto one that passed. nil disables rotation (no sandbox on
	// this platform), which is a real state and not a silent one — Core says so at
	// startup.
	rotate func(ctx context.Context, service, failed string)
	// gate proves a recipe in the isolated lane BEFORE this rule's traffic is
	// routed to the desync rung. nil applies immediately, which is the pre-v7.1
	// behaviour and the fallback where no sandbox exists.
	gate func(ctx context.Context, service string)
	log  *slog.Logger

	// mu serialises the whole-config recomposition. Enable (a service entering the
	// rung) and Reconcile (the periodic sweep that stops the engine when the rung
	// empties) both recompose from active(), and they must not interleave.
	mu sync.Mutex

	// chosen maps each service on the rung to the desync recipe currently applied,
	// for the rich /status. It has its own tiny lock so a status read never blocks
	// behind mu while an Apply is restarting nfqws.
	chosenMu sync.Mutex
	chosen   map[string]string
}

func (z *zapretExec) Class() string { return strategy.ClassZapret }

// chosenRecipe returns the desync recipe currently applied for service (empty when
// none), for the rich /status. It uses chosenMu, not mu, so a status read never
// waits on an in-flight nfqws (re)start.
func (z *zapretExec) chosenRecipe(service string) string {
	z.chosenMu.Lock()
	defer z.chosenMu.Unlock()
	return z.chosen[service]
}

// chosenPreset returns the upstream's own name for the bundle the running recipe
// came from ("ALT12"), or "" when the catalog does not record one. Operators
// reason in those names, so an id shown without it is a translation they have to
// do in their head every time.
func (z *zapretExec) chosenPreset(service string) string {
	id := z.chosenRecipe(service)
	if id == "" {
		return ""
	}
	for _, r := range z.recipes {
		if r.ID == id {
			return r.Preset
		}
	}
	return ""
}

// setChosen replaces the per-service recipe map with an independent copy, so a
// later mutation of the plan cannot race a status read.
func (z *zapretExec) setChosen(chosen map[string]string) {
	cp := make(map[string]string, len(chosen))
	for k, v := range chosen {
		cp[k] = v
	}
	z.chosenMu.Lock()
	z.chosen = cp
	z.chosenMu.Unlock()
}

// Enable is the rule ARRIVING on the desync rung. It records the operator's pin
// and hands off to the gate, which proves a recipe in the isolated lane before
// any of this rule's traffic is routed here.
//
// It does NOT apply anything itself any more. Applying first and judging after is
// how a wrong recipe used to reach the user: the rule was dropped onto it, the
// site broke, three probes later it escalated away. The rule now keeps whatever
// routing it already had — usually the tunnel, which works — until something is
// proven. The gate fails open, so a lane that cannot measure applies the top
// candidate exactly as before rather than leaving the rule unrouted.
func (z *zapretExec) Enable(ctx context.Context, service, strategyID string) error {
	z.mu.Lock()
	// The brain's resolved strategy for this rung. It used to be discarded — the
	// parameter was literally named `_` — so a `strategy_id` on a zapret chain step
	// pinned nothing at all: the composer ranked candidates by the knowledge base
	// and ran whatever it preferred. An operator who pinned ALT12 was measuring
	// something else, which is how a day of recipe experiments nearly went into the
	// record under the wrong names.
	if z.pinned == nil {
		z.pinned = map[string]string{}
	}
	if strategyID != "" {
		z.pinned[service] = strategyID
	} else {
		delete(z.pinned, service)
	}
	z.mu.Unlock()

	if z.gate == nil {
		return z.applyNow(ctx, service, strategyID)
	}
	gctx := ctx
	if z.life != nil {
		gctx = z.life()
	}
	go z.gate(gctx, service)
	return nil
}

// applyNow routes the service DIRECT and applies recipe — the only place a
// strategy becomes the thing in service. Called by the gate for a candidate it
// has proven, and directly when there is no gate.
//
// recipe wins over whatever the chain step pinned: a recipe that passed in the
// lane is evidence, and a config pin is a preference. `2ebb7a0` already refuses a
// pin that cannot do what its name says, so the two disagree only when the
// measurement found something better.
func (z *zapretExec) applyNow(ctx context.Context, service, recipe string) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if recipe != "" {
		if z.pinned == nil {
			z.pinned = map[string]string{}
		}
		z.pinned[service] = recipe
	}
	// Route direct FIRST: the service may be sitting on a VPN pool from a previous
	// chain step, and desyncing traffic that never leaves through the local stack
	// does nothing. SetSelector is idempotent, so re-entering the rung is cheap.
	if err := z.clash.SetSelector(ctx, registry.SelectorTag(service), "direct"); err != nil {
		return err
	}
	plan, err := z.composeAndApplyLocked(ctx)
	if err != nil {
		return err
	}
	if plan == nil {
		return nil // nothing composed (rung empty or uncovered) — no verdict to take
	}
	// Judge asynchronously: this is on the applier's path and must not block it for
	// the settle window.
	jctx := ctx
	if z.life != nil {
		jctx = z.life()
	}
	go z.judge(jctx, service, plan.Chosen)
	return nil
}

// pinFor reports the strategy currently pinned for a service, if any.
func (z *zapretExec) pinFor(service string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.pinned[service]
}

// Reconcile recomposes the desync to match the CURRENT rung membership, stopping
// the engine when the rung has emptied. It is the periodic counterpart to Enable:
// Enable reacts to a service arriving, Reconcile catches the departures the
// executor interface never reports. It takes no verdict — nothing new was chosen.
func (z *zapretExec) Reconcile(ctx context.Context) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	_, err := z.composeAndApplyLocked(ctx)
	return err
}

// composeAndApplyLocked recomposes the whole nfqws strategy from the services
// currently on the rung and applies it, or stops the engine when none remain. It
// returns the applied plan (for the caller to judge), or nil when nothing was
// applied. Caller holds z.mu.
func (z *zapretExec) composeAndApplyLocked(ctx context.Context) (*zaptune.Plan, error) {
	active := z.active()
	if len(active) == 0 {
		// The last service left the zapret rung. Stop desyncing rather than keep
		// mangling traffic for services that have moved to VPN (LOT-46): the executor
		// interface only reports a service ENTERING a rung, so without this sweep the
		// engine would run its final --new blocks indefinitely. Stop is idempotent, so
		// a rung that is simply always empty costs a cheap no-op each tick.
		if err := z.engine.Stop(ctx); err != nil {
			return nil, fmt.Errorf("zapret: stop idle engine: %w", err)
		}
		z.setChosen(nil)
		return nil, nil
	}
	plan := zaptune.ComposePinned(active, z.recipes, z.pick, z.resolve, z.hostlists, z.pinned)
	// Once per state change, not once per reconcile. The condition is static — a
	// knowledge-base entry naming a recipe this host does not have does not fix
	// itself — and the strategy is recomposed every few seconds, so repeating it
	// produced 124 warnings in two minutes and buried everything else in the
	// journal. A log that drowns the log is not a log.
	if z.warnedPins == nil {
		z.warnedPins = map[string]string{}
	}
	for _, p := range plan.PinsIgnored {
		if z.warnedPins[p.Service] == p.Want {
			continue
		}
		z.warnedPins[p.Service] = p.Want
		z.log.Warn("zapret: the pinned strategy is not usable for this service, picking instead — check the id and that the recipe renders here",
			"service", p.Service, "pinned", p.Want)
	}
	// Forget services whose pin is now honoured, so a recurrence is reported again
	// rather than silently swallowed by a stale memo.
	for svc := range z.warnedPins {
		still := false
		for _, p := range plan.PinsIgnored {
			if p.Service == svc {
				still = true
				break
			}
		}
		if !still {
			delete(z.warnedPins, svc)
		}
	}
	if !plan.Covered {
		// Composing a PARTIAL strategy is worse than composing none: a service whose
		// rule_set domains could not be resolved would be desynced for only its
		// inline domains, silently missing the rest (the discord-gateway regression).
		z.log.Warn("zapret: strategy not applied — some services are uncovered",
			"uncovered", plan.Uncovered)
		return nil, nil
	}
	// Write the hostlists BEFORE applying: nfqws must never be pointed at a file
	// that is not there yet. WriteIfChanged is the router's own primitive — atomic,
	// so nfqws cannot read a half-written list, and a no-op when the content is
	// identical, which matters because nfqws reloads on MTIME. Rewriting an
	// unchanged file would make it reload for nothing.
	changed, err := writeHostlists(plan.Hostlists)
	if err != nil {
		return nil, err
	}
	// nfqws resolves a bare payload name against ITS working directory, which is
	// ours rather than the zapret installation's.
	full := absolutizePayloads(plan.Args, z.files)
	restarted, err := z.engine.Apply(ctx, full)
	if err != nil {
		return nil, err
	}
	if changed > 0 && z.hostlists != "" {
		if restarted {
			// The argv changed too (or the engine had to be relaunched), so the
			// reload-free claim would be false — say what actually happened.
			z.log.Info("zapret: hostlist membership updated (engine re-applied)", "files", changed)
		} else {
			// Apply reported no restart: nfqws picks the new membership up from the file
			// itself, so the engine kept running and no live connection lost its desync.
			z.log.Info("zapret: hostlist membership updated without restarting the engine",
				"files", changed)
		}
	}
	// Only announce a real change. Enable (on every reassert) and the periodic
	// Reconcile both recompose from active() each tick; when nothing changed, Apply
	// is a no-op (restarted=false), and logging "desync applied" every tick from two
	// sources is just noise. A genuine (re)apply — a new service, a recipe change —
	// still reports at INFO.
	if restarted {
		z.log.Info("zapret: desync applied", "services", len(active), "chosen", plan.Chosen)
	} else {
		z.log.Debug("zapret: desync unchanged", "services", len(active))
	}
	z.setChosen(plan.Chosen)
	return &plan, nil
}

// zapretServices reports the services whose CURRENT brain position sits on a
// zapret-class chain step — the exact set nfqws must carry blocks for.
func (c *Core) zapretServices() []registry.Service {
	if c.brain == nil {
		return nil
	}
	var out []registry.Service
	for _, st := range c.brain.Snapshot() {
		svc, ok := c.reg.Services[st.Service]
		if !ok {
			continue
		}
		for _, step := range svc.Chain {
			if step.Position == st.Position && step.StrategyClass == strategy.ClassZapret {
				out = append(out, svc)
				break
			}
		}
	}
	return out
}

// renderableRecommender narrows the knowledge base's advice to strategies this
// machine can actually run.
//
// Without it the brain names ids the executor cannot render and silently falls
// back to its own pick. Measured on the ThinkPad: four rules had the brain asking
// for `alt12` — the ROUTER's script name, which is not a recipe in the catalog at
// all — while nfqws ran flowseal-alt12-google. And it is self-sustaining: the
// brain resolves the id, the prober records outcomes under it, the KB's confidence
// in it grows, the brain resolves it again. Nothing in that loop ever tries to
// render it, so nothing ever notices.
//
// Filtering at the source keeps the layering intact — the brain still knows
// nothing about recipes; the client, which owns the catalog, answers a narrower
// question on its behalf.
type renderableRecommender struct {
	kb    brain.Recommender
	known func(id string) bool
}

func (r renderableRecommender) TopNZapret(service string, n int, exclude ...string) []string {
	out := make([]string, 0, n)
	for _, id := range r.kb.TopNZapret(service, n, exclude...) {
		if r.known(id) {
			out = append(out, id)
		}
	}
	return out
}

// renderableKB wraps the live KB so the brain is only ever offered strategies
// with a recipe behind them on this host.
func (c *Core) renderableKB() brain.Recommender {
	return renderableRecommender{kb: c.kb, known: c.knownRecipe}
}

// setRenderableRecipes records the ids the desync executor can actually build,
// taken from the same filtered list it uses (catalog + config strategies, minus
// anything whose payload files are missing here).
func (c *Core) setRenderableRecipes(rs []strategycat.Recipe) {
	ids := make(map[string]bool, len(rs))
	for _, r := range rs {
		ids[r.ID] = true
	}
	c.recipeMu.Lock()
	c.recipeIDs = ids
	c.recipeMu.Unlock()
}

// knownRecipe reports whether a recipe with this id exists here. An EMPTY set
// means the desync executor was never built (no desync rung on this host), and
// then this must not filter anything: an empty answer would strip the knowledge
// base's advice for a reason that has nothing to do with the advice.
func (c *Core) knownRecipe(id string) bool {
	c.recipeMu.Lock()
	defer c.recipeMu.Unlock()
	if len(c.recipeIDs) == 0 {
		return true
	}
	return c.recipeIDs[id]
}

// writeHostlists puts each composed hostlist on disk and reports how many
// actually changed. Shared by the production apply and by the sandbox test,
// because a candidate measured against a DIFFERENT domain set than production
// would get is not a measurement of that candidate.
//
// WriteIfChanged is the router's own primitive — atomic, so nfqws cannot read a
// half-written list, and a no-op when the content is identical, which matters
// because nfqws reloads on MTIME and rewriting an unchanged file would make it
// reload for nothing.
func writeHostlists(lists map[string][]string) (int, error) {
	changed := 0
	for path, domains := range lists {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return changed, fmt.Errorf("zapret: hostlist dir: %w", err)
		}
		body := []byte(strings.Join(domains, "\n") + "\n")
		wrote, err := aggregate.WriteIfChanged(path, body, 0o644)
		if err != nil {
			return changed, fmt.Errorf("zapret: write hostlist %s: %w", path, err)
		}
		if wrote {
			changed++
		}
	}
	return changed, nil
}
