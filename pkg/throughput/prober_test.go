package throughput

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Bigger than the probe target on purpose: a body that ends early sets Short, and a
// Short reading describes the endpoint size rather than the path.
func bodyServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 32<<10)
		for i := 0; i < n; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The probe selector is SHARED with the live prober and the sandbox. A scan that
// leaves it aimed at the node it last measured makes the next probe of any rule
// report on a node the operator never chose — which is what "Lotsman started
// misbehaving" would actually mean, and it is not the ranking's fault.
func TestMeasureRestoresTheProbePath(t *testing.T) {
	srv := bodyServer(t, 32)
	var aimed string
	restores := 0
	p := &Prober{
		URLs:        []string{srv.URL},
		TargetBytes: 64 << 10,
		Settle:      0,
		ClientFor: func(_ context.Context, node string) (*http.Client, error) {
			aimed = node
			return srv.Client(), nil
		},
		Restore: func(_ context.Context, _ string) error {
			restores++
			aimed = "restored"
			return nil
		},
	}
	kbps, ok := p.Measure(context.Background(), "node-a")
	if !ok {
		t.Fatalf("measure failed: ok=false kbps=%v", kbps)
	}
	if kbps <= 0 {
		t.Errorf("kbps = %v, want a real rate", kbps)
	}
	if restores != 1 {
		t.Errorf("restores = %d, want 1", restores)
	}
	if aimed != "restored" {
		t.Errorf("probe path left aimed at %q", aimed)
	}
}

// A failed measurement still MOVED the selector, so it is exactly the case where
// forgetting the restore is most likely: the code path that returned early is the
// one nobody re-reads.
func TestMeasureRestoresEvenWhenTheNodeCarriesNothing(t *testing.T) {
	// A 204 has no body: the request succeeds and nothing is delivered, which is
	// the shape of a probe that measured zero.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	restores := 0
	p := &Prober{
		URLs:        []string{srv.URL},
		TargetBytes: 64 << 10,
		ClientFor: func(context.Context, string) (*http.Client, error) {
			return srv.Client(), nil
		},
		Restore: func(context.Context, string) error { restores++; return nil },
	}
	kbps, ok := p.Measure(context.Background(), "frozen")
	if ok {
		t.Errorf("a 204 was credited with %v KiB/s", kbps)
	}
	if restores != 1 {
		t.Errorf("restores = %d, want 1 even on a failed measurement", restores)
	}
}

func TestMeasureRestoresAfterACancelledContext(t *testing.T) {
	// The settle delay is where a cancel lands, and a context-aware implementation
	// returning here without restoring would leave the probe path on the last
	// scanned node. context.WithoutCancel is what makes the restore possible at all.
	srv := bodyServer(t, 32)
	restores := 0
	p := &Prober{
		URLs:        []string{srv.URL},
		TargetBytes: 64 << 10,
		Settle:      time.Hour,
		ClientFor: func(context.Context, string) (*http.Client, error) {
			return srv.Client(), nil
		},
		Restore: func(context.Context, string) error { restores++; return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := p.Measure(ctx, "node-b"); ok {
		t.Error("a cancelled measurement reported success")
	}
	if restores != 1 {
		t.Errorf("restores = %d, want 1 after cancel during settle", restores)
	}
}

func TestMeasureRefusesWithoutAClientOrURLs(t *testing.T) {
	// Silently reporting zero here would be read as a frozen exit and demoted,
	// when the truth is that nothing was configured.
	if _, ok := (*Prober)(nil).Measure(context.Background(), "n"); ok {
		t.Error("a nil prober measured")
	}
	if _, ok := (&Prober{URLs: []string{"http://x"}}).Measure(context.Background(), "n"); ok {
		t.Error("a prober with no ClientFor measured")
	}
}
