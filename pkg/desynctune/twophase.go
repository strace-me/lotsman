package desynctune

import (
	"context"
	"sort"
	"time"

	"github.com/strace-me/lotsman/pkg/desyncgen"
	"github.com/strace-me/lotsman/pkg/tester"
)

// TwoPhase searches in two passes: a cheap one over every candidate, then a
// throughput one over the few that survived.
//
// One pass cannot do both jobs. Judging on loss alone crowns a recipe that
// connects and then crawls, which is TSPU's signature failure and the exact thing
// worth avoiding. Judging every candidate on throughput instead means pulling
// real volume a hundred-odd times down the household's uplink, serialised — tens
// of minutes of the link being used to look for a strategy rather than to carry
// traffic.
//
// So: prescreen with the cheap probe, keep the best `keep` by loss, and spend the
// bytes only on those. Both passes go through tester.Resolve, which applies and
// probes arms strictly one at a time — that serialisation is a correctness
// requirement, not an efficiency one, because parallel handshakes to one SNI
// provoke the very freeze being measured. Space them with `settle`.
func TwoPhase(ctx context.Context, e desyncgen.Engine, cands []desyncgen.Strategy,
	apply func(context.Context, []string) error,
	cheap, burst tester.Probe, settle time.Duration, keep int, cfg tester.Config) (Result, error) {

	if keep < 1 {
		keep = 8
	}
	// Phase 1 is deliberately loss-only: a throughput gate here would reject
	// candidates before the cheap probe can even rank them, and the cheap probe
	// does not measure goodput to gate on.
	prescreen := cfg
	prescreen.MinGoodputKBps = 0

	survivors, err := rank(ctx, e, cands, apply, cheap, settle, prescreen, keep)
	if err != nil {
		return Result{}, err
	}
	if len(survivors) == 0 {
		// Nothing even connected. Reporting "no winner" is the honest answer;
		// inventing one from a phase that measured nothing is how a strategy
		// nobody verified reaches production.
		return Result{Verdict: tester.Verdict{Outcome: tester.OutcomeUnviable,
			Reason: "no candidate survived the prescreen"}}, nil
	}
	winner, v, err := Tune(ctx, e, survivors, apply, burst, settle, cfg)
	if err != nil {
		return Result{Verdict: v}, err
	}
	res := Result{Verdict: v, Winner: winner}
	if winner != nil {
		res.Args = e.Render(winner)
	}
	return res, nil
}

// rank runs the cheap pass and returns the best candidates by measured loss.
func rank(ctx context.Context, e desyncgen.Engine, cands []desyncgen.Strategy,
	apply func(context.Context, []string) error, probe tester.Probe,
	settle time.Duration, cfg tester.Config, keep int) ([]desyncgen.Strategy, error) {

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
		a := append([]string(nil), args...)
		arms = append(arms, tester.Arm{ID: id, Apply: func(c context.Context) error { return apply(c, a) }})
	}
	_, trials, err := tester.Resolve(ctx, baseline, arms, probe, settle, cfg)
	if err != nil {
		return nil, err
	}
	// A trial with no samples measured nothing; keeping it would spend the
	// expensive pass on a candidate we have no evidence about at all.
	usable := trials[:0]
	for _, t := range trials {
		if t.Q.Samples > 0 && t.Q.Loss < 1 {
			usable = append(usable, t)
		}
	}
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].Q.Loss < usable[j].Q.Loss })
	if len(usable) > keep {
		usable = usable[:keep]
	}
	out := make([]desyncgen.Strategy, 0, len(usable))
	for _, t := range usable {
		out = append(out, byID[t.RecipeID])
	}
	return out, nil
}
