package core

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

// fakeEngine records how the executor drove the desync engine, standing in for a
// live NFQUEUE the test cannot install.
type fakeEngine struct {
	applied  [][]string
	stops    int
	stopErr  error
	applyErr error
}

func (f *fakeEngine) Apply(_ context.Context, args []string) (bool, error) {
	if f.applyErr != nil {
		return false, f.applyErr
	}
	f.applied = append(f.applied, args)
	return true, nil
}

func (f *fakeEngine) Stop(context.Context) error {
	f.stops++
	return f.stopErr
}

// The whole point of the periodic Reconcile: when the last service has left the
// zapret rung, the engine must be stopped, not left desyncing traffic for
// services that moved to VPN long ago (LOT-46). The executor interface only
// reports arrivals, so nothing else closes this gap.
func TestReconcileStopsTheEngineWhenTheRungEmpties(t *testing.T) {
	eng := &fakeEngine{}
	z := &zapretExec{
		engine: eng,
		active: func() []registry.Service { return nil }, // rung emptied
		log:    slog.New(slog.DiscardHandler),
	}

	if err := z.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if eng.stops != 1 {
		t.Errorf("engine stopped %d times, want 1 — an empty rung must stop the desync", eng.stops)
	}
	if len(eng.applied) != 0 {
		t.Errorf("applied a strategy for an empty rung: %v", eng.applied)
	}
}

// A stop failure (e.g. the nft table could not be removed) must surface so the
// operator learns traffic may still be queued, not be swallowed.
func TestReconcileSurfacesAStopFailure(t *testing.T) {
	eng := &fakeEngine{stopErr: errors.New("nft delete failed")}
	z := &zapretExec{
		engine: eng,
		active: func() []registry.Service { return nil },
		log:    slog.New(slog.DiscardHandler),
	}
	if err := z.Reconcile(context.Background()); err == nil {
		t.Error("a failed engine stop must be reported, not swallowed")
	}
}
