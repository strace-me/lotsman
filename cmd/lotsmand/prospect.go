package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/burstprobe"
	"github.com/strace-me/lotsman/pkg/dataplane"
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
	log      *slog.Logger
}

// run performs at most one pass. It is a periodic.Task body: refusing is the
// normal outcome and must never be an error, or a quiet box would log a failure
// every tick.
func (p *prospector) run(ctx context.Context) error {
	if dataplane.RealtimeActive(ctx, p.clash) {
		return nil // somebody is playing; the search can wait hours
	}
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

	engine := zapret.NfqwsEngine{}
	// Cheap first over everything, volume only over the survivors. Both probes
	// dial the same path a service would, so a strategy that wins here won on
	// the route it will actually serve.
	cheap := p.probe(svc, 8<<10)
	burst := p.probe(svc, 64<<10)

	res, err := desynctune.TwoPhase(ctx, engine, desynctune.Candidates(engine, nil, 48),
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
	p.store.Won(id, res.Args, svc.Name)
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
