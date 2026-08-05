package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/burstprobe"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/desyncgen"
	"github.com/strace-me/lotsman/pkg/desynctune"
	"github.com/strace-me/lotsman/pkg/prospect"
	"github.com/strace-me/lotsman/pkg/quality"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/tester"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// prospector searches, in the background and at idle, for desync strategies this
// box can prove for itself.
//
// It is not a crisis response. Every recipe in the catalog came from a bundle
// somebody else maintains, and if that bundle stops being updated — or its
// author's line stops resembling this one — the box has nothing of its own. A few
// self-proven strategies a week is a hedge against that, and it costs only idle
// time.
//
// Everything about the pacing is chosen so the household never notices: a pass
// starts only when no live UDP session is on the link, the gate admits one pass
// at a time with a long cooldown, and the search itself runs in a sandbox whose
// engine no real traffic ever reaches.
type prospector struct {
	reg      *registry.Registry
	brain    *brain.Brain
	clash    *dataplane.ClashClient
	store    *prospect.Store
	gate     *desynctune.Gate
	sandbox  *zapret.Sandbox
	probeVia string // socks addr the measurements dial through
	// healthURL is the baseline every strategy is measured against: can this box
	// reach the internet with no desync at all. It gates the collapse detector.
	healthURL      string
	collapseWindow time.Duration
	collapseMin    int
	log            *slog.Logger
}

// run performs at most one pass. It is a periodic.Task body: refusing is the
// normal outcome and must never be an error, or a quiet box would log a failure
// every tick.
func (p *prospector) run(ctx context.Context) error {
	if dataplane.RealtimeActive(ctx, p.clash) {
		return nil // somebody is playing; the search can wait hours
	}
	p.checkCollapse(ctx)
	svc, ok := p.pick()
	if !ok {
		return nil
	}
	release, ok := p.gate.Begin(svc.Name)
	if !ok {
		return nil
	}
	defer release()
	defer func() {
		if err := p.sandbox.Close(ctx); err != nil {
			p.log.Warn("prospect: sandbox teardown", "err", err)
		}
	}()

	// A lead first, always. A discovery pass generates candidates fresh, so a
	// strategy that won once may never be offered again — and would sit at one
	// measurement forever while the store looked busy. Confirming is also the
	// cheaper pass: a handful of known arms rather than a grid of fifty.
	if leads := p.store.Leads(); len(leads) > 0 {
		return p.verify(ctx, svc, leads)
	}

	engine := zapret.NfqwsEngine{}
	// Cheap first over everything, volume only over the survivors. Both probes
	// dial the same path a service would, so a strategy that wins here won on
	// the route it will actually serve.
	cheap := p.probe(svc, 8<<10)
	burst := p.probe(svc, 64<<10)

	// Start next to something that has held, not at index 0. The neighbourhood of
	// a strategy proven over weeks is where the next working one most likely
	// lives, and consulting the record costs no probes at all. Cold grid only
	// when there is nothing proven yet.
	var seed desyncgen.Strategy
	if m, ok := p.store.StableSeed(); ok {
		seed = m
		p.log.Info("prospect: searching around a proven strategy", "service", svc.Name)
	}
	res, err := desynctune.TwoPhase(ctx, engine, desynctune.Candidates(engine, seed, 48),
		p.sandbox.Apply, cheap, burst, 2*time.Second, 6, tester.ThroughputConfig())
	if err != nil {
		p.log.Warn("prospect: pass failed", "service", svc.Name, "err", err)
		return nil // a failed search is not a daemon failure
	}
	if !res.Viable() {
		p.log.Info("prospect: nothing beat the baseline", "service", svc.Name, "reason", res.Verdict.Reason)
		return nil
	}

	id := desynctune.StrategyID(res.Args)
	p.store.Won(id, res.Args, svc.Name, res.Winner)
	if err := p.store.Save(); err != nil {
		p.log.Warn("prospect: could not persist the finding", "err", err)
	}
	// Deliberately not promoted here. One win is a lead; the store promotes it
	// only after a second, separate pass agrees — which is why this logs a find
	// and not a decision.
	p.log.Info("prospect: strategy won a pass", "service", svc.Name, "id", id,
		"confirmed", len(p.store.Recipes()), "pending", p.store.Pending())
	return nil
}

// checkCollapse notices when several proven strategies stopped working at once.
//
// Independent strategies do not die together by chance, so the correlation is
// the signal: something upstream changed and every recorded failure now describes
// a network that no longer exists. Forgetting those failures is worth more than
// any calendar threshold.
//
// Gated on the link being otherwise healthy, and that gate is the whole safety of
// it. A dead uplink or a changed exit fails everything at once too, and a
// detector that cannot tell the difference would erase months of evidence during
// a five-minute ISP outage — silently, while reporting success.
func (p *prospector) checkCollapse(ctx context.Context) {
	if !p.store.Collapsed(time.Now(), p.collapseWindow, p.collapseMin) {
		return
	}
	if !p.linkHealthy(ctx) {
		p.log.Info("prospect: several strategies fell out together, but the link itself is unwell — not treating it as a DPI change")
		return
	}
	n := p.store.Rearm()
	if err := p.store.Save(); err != nil {
		p.log.Warn("prospect: could not persist the rearm", "err", err)
	}
	p.log.Warn("prospect: proven strategies collapsed together while the link is healthy — the DPI moved; forgetting stale failures and searching again",
		"rearmed", n, "window", p.collapseWindow.String())
}

// linkHealthy asks whether the box can reach the internet at all without any
// desync, which is the baseline every strategy is measured against.
func (p *prospector) linkHealthy(ctx context.Context) bool {
	q := burstprobe.Probe(ctx, dataplane.BurstClient(p.probeVia, 10*time.Second),
		[]string{p.healthURL}, 8<<10, 1)
	return q.Samples > 0 && q.Loss < 1
}

// verify re-measures leads against the no-desync baseline. A lead that wins here
// is confirmed by the store and joins the pool; one that loses is recorded as
// such, and enough losses drop it — a strategy that cannot repeat was luck, and
// luck is exactly what the second pass exists to catch.
func (p *prospector) verify(ctx context.Context, svc registry.Service, leads []prospect.Finding) error {
	baseline := tester.Arm{ID: "no-desync", Apply: func(c context.Context) error { return p.sandbox.Apply(c, nil) }}
	arms := make([]tester.Arm, 0, len(leads))
	for _, f := range leads {
		args := append([]string(nil), f.Args...)
		arms = append(arms, tester.Arm{ID: f.ID, Apply: func(c context.Context) error { return p.sandbox.Apply(c, args) }})
	}
	v, trials, err := tester.Resolve(ctx, baseline, arms, p.probe(svc, 64<<10), 2*time.Second, tester.ThroughputConfig())
	if err != nil {
		p.log.Warn("prospect: verification pass failed", "service", svc.Name, "err", err)
		return nil
	}
	byID := map[string][]string{}
	for _, f := range leads {
		byID[f.ID] = f.Args
	}
	for _, t := range trials {
		// Only the winner is credited. Crediting every arm that merely connected
		// would confirm strategies on the strength of a link that was working
		// anyway, which is the whole failure the second pass exists to prevent.
		if t.RecipeID == v.RecipeID && v.Outcome == tester.OutcomeRecipe {
			p.store.Won(t.RecipeID, byID[t.RecipeID], svc.Name, nil)
			continue
		}
		p.store.Lost(t.RecipeID)
	}
	if err := p.store.Save(); err != nil {
		p.log.Warn("prospect: could not persist verification", "err", err)
	}
	p.log.Info("prospect: verified leads", "service", svc.Name, "tested", len(trials),
		"winner", v.RecipeID, "confirmed", len(p.store.Recipes()), "pending", p.store.Pending())
	return nil
}

// pick chooses a service currently sitting on a zapret rung: the only ones whose
// desync a finding could ever serve.
func (p *prospector) pick() (registry.Service, bool) {
	for _, svc := range p.reg.Services {
		pos := p.brain.Position(svc.Name)
		if pos < 0 || pos >= len(svc.Chain) {
			continue
		}
		if svc.Chain[pos].StrategyClass == strategy.ClassZapret {
			return svc, true
		}
	}
	return registry.Service{}, false
}

// probe measures the service through the sandbox: the dialer stamps the tune
// mark, so these packets — and only these — reach the candidate engine.
func (p *prospector) probe(svc registry.Service, target int64) tester.Probe {
	url := svc.ProbeTarget
	return func(ctx context.Context) quality.Quality {
		if url == "" {
			return quality.Quality{}
		}
		return burstprobe.Probe(ctx, dataplane.MarkedClient(zapret.TuneMark, p.probeVia, 15*time.Second),
			[]string{url}, target, 1)
	}
}

func prospectStore(path string, log *slog.Logger) (*prospect.Store, error) {
	s, err := prospect.Open(path, nil)
	if err != nil {
		return nil, fmt.Errorf("prospect store: %w", err)
	}
	log.Info("prospecting for self-proven strategies", "store", path,
		"confirmed", len(s.Recipes()), "pending", s.Pending())
	return s, nil
}
