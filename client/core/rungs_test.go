package core

import (
	"context"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/executor"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// A rung dropped because its executor happened to be unavailable must come back when
// the executor does. Writing the trimmed chain into the shared registry made the loss
// permanent: the desync engine gives up when there is no default route — exactly a
// laptop's state for the first seconds after a resume — and the zapret rung then stayed
// gone until the process restarted, which it never does.
func TestDroppedRungsReturnWhenTheirExecutorDoes(t *testing.T) {
	conf, err := config.Parse([]byte(`
services:
  - name: youtube
    category: streaming
    probe_target: https://x
    domains: [youtube.com]
`))
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{
		conf: conf, reg: conf.Registry, log: slog.New(slog.DiscardHandler),
		pristineChains: snapshotChains(conf.Registry),
	}
	full := len(c.reg.Services["youtube"].Chain)
	if full < 2 {
		t.Fatalf("fixture needs a multi-rung chain, got %d", full)
	}

	// The desync executor is missing this pass (no default route yet).
	c.dropUnsupportedRungs([]executor.StrategyExecutor{vpnOnly{}})
	trimmed := len(c.reg.Services["youtube"].Chain)
	if trimmed >= full {
		t.Fatalf("a missing executor should trim the chain: %d -> %d", full, trimmed)
	}

	// It comes back on the next rebuild.
	c.dropUnsupportedRungs([]executor.StrategyExecutor{vpnOnly{}, zapretOnly{}, emergencyOnly{}})
	if got := len(c.reg.Services["youtube"].Chain); got != full {
		t.Errorf("chain = %d rungs, want the original %d — a transient miss must not be permanent", got, full)
	}
	if !c.hasZapretStep() {
		t.Error("the desync rung must be back, or the engine is never rebuilt")
	}
}

type vpnOnly struct{}

func (vpnOnly) Class() string                                { return strategy.ClassVPN }
func (vpnOnly) Enable(context.Context, string, string) error { return nil }

type zapretOnly struct{}

func (zapretOnly) Class() string                                { return strategy.ClassZapret }
func (zapretOnly) Enable(context.Context, string, string) error { return nil }

type emergencyOnly struct{}

func (emergencyOnly) Class() string                                { return strategy.ClassEmergency }
func (emergencyOnly) Enable(context.Context, string, string) error { return nil }
