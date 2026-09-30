package core

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/registry"
)

// A fake engine that records every change it is asked to make.
type recordingEngine struct {
	applied []string
	stopped int
	plan    string
}

func (e *recordingEngine) Apply(_ context.Context, argv []string) (bool, error) {
	e.applied = append(e.applied, strings.Join(argv, " "))
	return true, nil
}
func (e *recordingEngine) Stop(context.Context) error { e.stopped++; return nil }
func (e *recordingEngine) Alive() bool                { return true }

// dry-run must mean NOTHING MOVED. The selector executors already took the flag
// from the start; the desync engine was the writer with no gate, so an observe-only
// client still rewrote nft and restarted nfqws — the precise surprise the flag
// exists to prevent, and the gap the flag-parity guard in pkg/contract found.
func TestDryRunNeverTouchesTheDesyncEngine(t *testing.T) {
	eng := &recordingEngine{}
	z := &zapretExec{engine: eng, dryRun: true, log: slog.New(slog.DiscardHandler),
		active: func() []registry.Service { return nil }}
	_ = registry.Service{Name: "youtube", Chain: []registry.ChainStep{{
		State: registry.StatePreferred, StrategyClass: "zapret", StrategyID: "alt12",
	}}}

	if err := z.Enable(context.Background(), "youtube", "alt12"); err != nil {
		t.Fatalf("dry-run Enable returned an error: %v", err)
	}
	if len(eng.applied) != 0 {
		t.Errorf("dry-run applied a strategy: %v", eng.applied)
	}
	if eng.stopped != 0 {
		t.Errorf("dry-run stopped the engine %d time(s)", eng.stopped)
	}

	// And the inverse: without the flag the engine really is driven, otherwise this
	// test would pass with a gate that swallows everything.
	// The live arm needs a clash endpoint, because flipping the service selector is
	// a real call in a live run. An httptest server is enough: the point is that the
	// gate is not swallowing everything, not that sing-box is running.
	flips := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			flips++
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	live := &recordingEngine{}
	z2 := &zapretExec{engine: live, dryRun: false, log: slog.New(slog.DiscardHandler),
		clash:  dataplane.NewClashClient(srv.URL, ""),
		active: func() []registry.Service { return nil }}
	_ = z2.Enable(context.Background(), "youtube", "alt12")
	if len(live.applied) == 0 && live.stopped == 0 {
		t.Error("a live run made no change either, so the dry-run gate is untested")
	}
	if flips == 0 {
		t.Error("a live run never flipped a selector, so the dry-run selector gate is untested")
	}
}

func TestDryRunStopsTheIdleEngineOnlyInALiveRun(t *testing.T) {
	// A service leaving the desync rung stops the engine (LOT-46). In dry-run the
	// engine must be left alone, or the "nothing moves" promise is broken at
	// exactly the moment a run would otherwise be silent.
	eng := &recordingEngine{}
	z := &zapretExec{engine: eng, dryRun: true, log: slog.New(slog.DiscardHandler),
		active: func() []registry.Service { return nil }}
	// Enable with no service on a zapret rung is the idle sweep.
	if err := z.Enable(context.Background(), "unrelated", ""); err != nil {
		t.Fatalf("idle Enable returned an error: %v", err)
	}
	if eng.stopped != 0 {
		t.Errorf("dry-run stopped the idle engine %d time(s)", eng.stopped)
	}
}
