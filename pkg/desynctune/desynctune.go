// Package desynctune is the tuner that closes the generator loop (LOT-31 v7c):
// it turns generated desync STRATEGIES into an A/B and asks pkg/tester which one
// (if any) actually beats the no-desync baseline against the live service. It is
// the bridge between the engine-agnostic generator (pkg/desyncgen) and the
// measured decision engine (pkg/tester).
//
// Both the data-plane apply and the probe are injected, so this stays unit-
// testable and engine/platform-agnostic: the caller wires the ISOLATED apply (own
// qnum + temp nft chain on a router, a scratch instance elsewhere — LOT-10d) and
// the service probe. The winner can then be promoted to a recipe + recorded in
// the KB; if nothing is viable the verdict says so and Brain escalates the rung.
package desynctune

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/desyncgen"
	"github.com/strace-me/lotsman/pkg/tester"
)

// StrategyID is a stable, readable-ish id for a rendered strategy (its desync arg
// tokens) — deterministic across runs so the KB can track the same strategy.
func StrategyID(args []string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.Join(args, " ")))
	return fmt.Sprintf("gen-%08x", h.Sum32())
}

// Tune A/B-tests the candidate strategies against the no-desync baseline for one
// service and returns the winning strategy (or nil when no recipe is viable),
// plus the tester verdict. apply(ctx, nil) must put the data plane into the clean
// no-desync state (baseline); apply(ctx, e.Render(s)) applies a candidate's
// desync IN ISOLATION. probe measures the service in the current state. settle/cfg
// are passed through to tester.Resolve.
//
// Candidates that render to nothing (no method) or collide on id are skipped. A
// candidate whose apply errors simply contributes no trial (tester.Resolve drops
// it); a baseline apply error is fatal (returned).
func Tune(ctx context.Context, e desyncgen.Engine, cands []desyncgen.Strategy, apply func(context.Context, []string) error, probe tester.Probe, settle time.Duration, cfg tester.Config) (desyncgen.Strategy, tester.Verdict, error) {
	byID := make(map[string]desyncgen.Strategy, len(cands))
	baseline := tester.Arm{ID: "no-desync", Apply: func(c context.Context) error { return apply(c, nil) }}
	arms := make([]tester.Arm, 0, len(cands))
	for _, s := range cands {
		args := e.Render(s)
		if len(args) == 0 {
			continue
		}
		id := StrategyID(args)
		if _, dup := byID[id]; dup {
			continue
		}
		byID[id] = s
		applyArgs := append([]string(nil), args...) // capture per-iteration
		arms = append(arms, tester.Arm{ID: id, Apply: func(c context.Context) error { return apply(c, applyArgs) }})
	}

	v, _, err := tester.Resolve(ctx, baseline, arms, probe, settle, cfg)
	if err != nil {
		return nil, v, err
	}
	// byID[""] is absent -> nil when the verdict is NoDesync/Unviable (no winner).
	return byID[v.RecipeID], v, nil
}
