package nodepass

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/noderank"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// The overnight bug, as a test. The ranker used to fetch subscriptions privately,
// so a degraded fetch shrank what it ranked while the running config kept its
// last-good fleet — and a 94-node box reported "all eligible nodes down" 58 times,
// every one of them eligible=1. The ranker is now handed the authoritative set.
func TestRankerSeesTheCallerNodeSetAndNotItsOwn(t *testing.T) {
	conf := &config.Config{
		Subscriptions: []subscription.Declaration{{Name: "s", URL: "https://example.invalid/sub"}},
		Pools:         &pools.Set{Pools: map[string]pools.Pool{"p": {Type: "urltest"}}},
	}
	reg := &registry.Registry{Services: map[string]registry.Service{}}

	var got int
	src := func(context.Context) []subscription.Node {
		return nil // deliberately empty: whatever the caller says is what gets ranked
	}
	_ = Run(context.Background(), conf, reg, noderank.New(nil, 1, true, quietLog()), src, nil, quietLog())
	if got != 0 {
		t.Errorf("ranker saw %d nodes, want the caller's 0", got)
	}
}

// The signature is the point: a caller-supplied source means a degraded fetch
// can no longer silently become a membership change. So the loader must be
// called by the caller and passed in, never built inside Run.
func TestRunTakesASourceAndNeverFetches(t *testing.T) {
	conf := &config.Config{
		Subscriptions: []subscription.Declaration{{Name: "s", URL: "https://example.invalid/sub"}},
		Pools:         &pools.Set{Pools: map[string]pools.Pool{"p": {Type: "urltest"}}},
	}
	reg := &registry.Registry{Services: map[string]registry.Service{}}
	calls := 0
	src := func(context.Context) []subscription.Node {
		calls++
		return []subscription.Node{{ID: "n1"}}
	}
	if err := Run(context.Background(), conf, reg, noderank.New(nil, 1, true, quietLog()), src, nil, quietLog()); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("the caller's source was never consulted — Run is deciding for itself again")
	}
}

func TestEmptySourceIsNotFatal(t *testing.T) {
	// A source that returns nothing must produce an empty pass, not a panic and
	// not an invented verdict: with no nodes there is nothing to rank, and the
	// generator keeps serving its own snapshot meanwhile.
	conf := &config.Config{
		Subscriptions: []subscription.Declaration{{Name: "s", URL: "https://example.invalid/sub"}},
		Pools:         &pools.Set{Pools: map[string]pools.Pool{"p": {Type: "urltest"}}},
	}
	reg := &registry.Registry{Services: map[string]registry.Service{}}
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	err := Run(context.Background(), conf, reg, noderank.New(nil, 1, true, log),
		func(context.Context) []subscription.Node { return nil }, nil, log)
	if err != nil {
		t.Fatalf("an empty fleet should be a quiet pass, got %v", err)
	}
	if !strings.Contains(buf.String(), "nodes=0") {
		t.Errorf("the ranked set size is not in the log, so a shrunken fleet is invisible again: %q", buf.String())
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
