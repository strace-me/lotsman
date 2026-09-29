package throughput

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func bigBody(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 32<<10)
		for i := 0; i < 32; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeSelector models sing-box's select-by-NAME path: aiming it is a visible
// state change that everything else on that path inherits.
type fakeSelector struct {
	aimed     string
	failAim   map[string]bool
	failBack  bool
	aimedSeen []string
}

func (f *fakeSelector) PointAt(_ context.Context, node string) (string, error) {
	f.aimedSeen = append(f.aimedSeen, node)
	// failBack is keyed on the ORIGIN rather than on "", because that is how a
	// restore is actually issued: to the value found before the aim.
	if f.failBack && node == "vpn_url_test" {
		return "", errors.New("restore refused")
	}
	if f.failAim[node] {
		return "", errors.New("no such outbound")
	}
	prev := f.aimed
	f.aimed = node
	return prev, nil
}
func (f *fakeSelector) Current(context.Context) (string, error) { return f.aimed, nil }

func TestSelectorProberRestoresWhatItFound(t *testing.T) {
	srv := bigBody(t)
	sel := &fakeSelector{aimed: "vpn_url_test"} // whatever the canary left aimed
	p := &SelectorProber{
		Selector:  sel,
		ClientFor: func(context.Context) (*http.Client, error) { return srv.Client(), nil },
		URLs:      []string{srv.URL},
		Settle:    0,
	}
	kbps, ok := p.Measure(context.Background(), "node-a")
	if !ok || kbps <= 0 {
		t.Fatalf("measure = %v ok=%v", kbps, ok)
	}
	// The value found beforehand, not "something sensible": restoring to a default
	// instead of what was there is how a scan silently re-aims the canary.
	if sel.aimed != "vpn_url_test" {
		t.Errorf("probe path left aimed at %q, want the previous %q", sel.aimed, "vpn_url_test")
	}
	if len(sel.aimedSeen) != 2 || sel.aimedSeen[0] != "node-a" || sel.aimedSeen[1] != "vpn_url_test" {
		t.Errorf("aim sequence = %v, want [node-a vpn_url_test]", sel.aimedSeen)
	}
}

func TestSelectorProberRestoresWhenTheNodeIsUnaimable(t *testing.T) {
	srv := bigBody(t)
	sel := &fakeSelector{failAim: map[string]bool{"dead": true}}
	p := &SelectorProber{
		Selector:  sel,
		ClientFor: func(context.Context) (*http.Client, error) { return srv.Client(), nil },
		URLs:      []string{srv.URL},
	}
	if _, ok := p.Measure(context.Background(), "dead"); ok {
		t.Error("an unaimable node measured")
	}
	// Aiming failed, so the path was never moved and there is nothing to restore.
	// Asserting that matters: a blind restore here would aim it at "" and break
	// the canary on a node the scan never touched.
	if len(sel.aimedSeen) != 1 {
		t.Errorf("aim sequence = %v, want a single attempt", sel.aimedSeen)
	}
}

// A restore that FAILS leaves the probe path aimed at the node the scan measured.
// That is the exact state the type exists to prevent, so it must reach the caller
// loudly — and the measurement is still not a verdict, because we do not know what
// path it came out of.
func TestSelectorProberReportsAFailedRestore(t *testing.T) {
	srv := bigBody(t)
	sel := &fakeSelector{failBack: true, aimed: "vpn_url_test"}
	var reported error
	p := &SelectorProber{
		Selector:  sel,
		ClientFor: func(context.Context) (*http.Client, error) { return srv.Client(), nil },
		URLs:      []string{srv.URL},
		OnRestore: func(err error) { reported = err },
	}
	if _, ok := p.Measure(context.Background(), "node-b"); ok {
		t.Error("a measurement was credited although its path was never restored")
	}
	if reported == nil {
		t.Fatal("a failed restore was swallowed: the probe path is left aimed at node-b")
	}
}

func TestSelectorProberRestoresAfterCancel(t *testing.T) {
	srv := bigBody(t)
	sel := &fakeSelector{aimed: "vpn_url_test"}
	p := &SelectorProber{
		Selector:  sel,
		ClientFor: func(context.Context) (*http.Client, error) { return srv.Client(), nil },
		URLs:      []string{srv.URL},
		Settle:    time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := p.Measure(ctx, "node-c"); ok {
		t.Error("a cancelled measurement reported success")
	}
	if sel.aimed != "vpn_url_test" {
		t.Errorf("cancel during settle left the path aimed at %q", sel.aimed)
	}
}
