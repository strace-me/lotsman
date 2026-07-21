package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/strace-me/lotsman/pkg/aggregate"

	"github.com/strace-me/lotsman/client/platform/nfqws"
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
type zapretExec struct {
	clash     *dataplane.ClashClient
	engine    *nfqws.Engine
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
	life   func() context.Context
	canary func(ctx context.Context, service string) bool
	record func(service, recipe string, ok bool)
	log    *slog.Logger
}

func (z *zapretExec) Class() string { return strategy.ClassZapret }

// Enable routes the service direct and re-applies the composed desync strategy.
func (z *zapretExec) Enable(ctx context.Context, service, _ string) error {
	// Route direct FIRST: the service may be sitting on a VPN pool from a previous
	// chain step, and desyncing traffic that never leaves through the local stack
	// does nothing. SetSelector is idempotent, so re-entering the rung is cheap.
	if err := z.clash.SetSelector(ctx, registry.SelectorTag(service), "direct"); err != nil {
		return err
	}

	active := z.active()
	if len(active) == 0 {
		return nil
	}
	plan := zaptune.Compose(active, z.recipes, z.pick, z.resolve, z.hostlists)
	if !plan.Covered {
		// Composing a PARTIAL strategy is worse than composing none: a service whose
		// rule_set domains could not be resolved would be desynced for only its
		// inline domains, silently missing the rest (the discord-gateway regression).
		z.log.Warn("zapret: strategy not applied — some services are uncovered",
			"uncovered", plan.Uncovered, "service", service)
		return nil
	}
	// Write the hostlists BEFORE applying: nfqws must never be pointed at a file
	// that is not there yet. WriteIfChanged is the router's own primitive — atomic,
	// so nfqws cannot read a half-written list, and a no-op when the content is
	// identical, which matters because nfqws reloads on MTIME. Rewriting an
	// unchanged file would make it reload for nothing.
	changed := 0
	for path, domains := range plan.Hostlists {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("zapret: hostlist dir: %w", err)
		}
		body := []byte(strings.Join(domains, "\n") + "\n")
		wrote, err := aggregate.WriteIfChanged(path, body, 0o644)
		if err != nil {
			return fmt.Errorf("zapret: write hostlist %s: %w", path, err)
		}
		if wrote {
			changed++
		}
	}
	// nfqws resolves a bare payload name against ITS working directory, which is
	// ours rather than the zapret installation's.
	if err := z.engine.Apply(ctx, absolutizePayloads(plan.Args, z.files)); err != nil {
		return err
	}
	if changed > 0 && z.hostlists != "" {
		// The arguments did not have to change for this to take effect: nfqws picks
		// the new membership up from the file itself, so the engine kept running and
		// no live connection lost its desync.
		z.log.Info("zapret: hostlist membership updated without restarting the engine",
			"files", changed)
	}
	z.log.Info("zapret: desync applied", "services", len(active), "chosen", plan.Chosen)
	// Judge asynchronously: Enable is on the applier's path and must not block it
	// for the settle window.
	jctx := ctx
	if z.life != nil {
		jctx = z.life()
	}
	go z.judge(jctx, service, plan.Chosen)
	return nil
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
