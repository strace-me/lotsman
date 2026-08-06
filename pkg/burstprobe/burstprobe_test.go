package burstprobe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/tester"
)

// a server that streams `total` bytes, pausing `stall` after the first `cliff`
// bytes — models the TSPU freeze (fast to ~16KB, then a crawl).
func server(t *testing.T, total, cliff int64, stall time.Duration) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		var sent int64
		for sent < total {
			if cliff > 0 && sent >= cliff && stall > 0 {
				select { // the freeze — but bail when the client disconnects (its timeout)
				case <-time.After(stall):
				case <-r.Context().Done():
					return
				}
			}
			if _, err := w.Write(buf); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			sent += int64(len(buf))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func client(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }

// A healthy endpoint streams fast: high goodput, no loss, and Decide calls the
// baseline NOT blocked (NoDesync).
func TestProbeHealthyPathIsFast(t *testing.T) {
	srv := server(t, 256<<10, 0, 0)
	q := Probe(context.Background(), client(2*time.Second), []string{srv.URL}, 64<<10, 5)
	if q.GoodputKBps < 100 || q.Loss != 0 {
		t.Fatalf("healthy path: goodput=%.0f loss=%.2f, want fast+lossless", q.GoodputKBps, q.Loss)
	}
	if v := tester.Decide(q, nil, tester.ThroughputConfig()); v.Outcome != tester.OutcomeNoDesync {
		t.Fatalf("fast baseline should be NoDesync, got %s", v.Outcome)
	}
}

// A frozen endpoint serves ~16KB then crawls: the read stalls past the client
// timeout, goodput collapses, and the throughput gate refuses to call it healthy
// (with no working recipe -> Unviable, so Brain escalates). This is the freeze
// that loss-only probing could not see.
func TestProbeFrozenPathFailsThroughputGate(t *testing.T) {
	srv := server(t, 1<<20, 16<<10, 3*time.Second)
	q := Probe(context.Background(), client(400*time.Millisecond), []string{srv.URL}, 128<<10, 5)
	if q.GoodputKBps > 64 {
		t.Fatalf("frozen path should collapse goodput, got %.0f KiB/s", q.GoodputKBps)
	}
	if v := tester.Decide(q, nil, tester.ThroughputConfig()); v.Outcome != tester.OutcomeUnviable {
		t.Fatalf("frozen baseline must be Unviable under the gate, got %s (%s)", v.Outcome, v.Reason)
	}
}

// Multi-endpoint: one fast + one frozen domain must be judged by the WEAKEST —
// the single-canary Goodhart defeated.
func TestProbeWorstCaseAcrossEndpoints(t *testing.T) {
	fast := server(t, 256<<10, 0, 0)
	frozen := server(t, 1<<20, 16<<10, 3*time.Second)
	q := Probe(context.Background(), client(400*time.Millisecond), []string{fast.URL, frozen.URL}, 128<<10, 5)
	if q.GoodputKBps > 64 {
		t.Fatalf("worst-case across endpoints should reflect the frozen one, got %.0f", q.GoodputKBps)
	}
}

// readN returns partial bytes on a stall rather than discarding them (so a freeze
// scores as low goodput, not just loss).
func TestReadNReturnsPartialOnStall(t *testing.T) {
	srv := server(t, 1<<20, 16<<10, 2*time.Second)
	n, _, ended := readN(context.Background(), client(300*time.Millisecond), srv.URL, 512<<10)
	if n <= 0 || n > 64<<10 {
		t.Fatalf("partial read on stall = %d bytes, want a partial chunk (~16KB)", n)
	}
	// And it must NOT read as end-of-body: a stall is the path failing, which is
	// precisely what must stay distinguishable from a small file.
	if ended {
		t.Error("a stalled read must not report end-of-body — that is how a freeze gets excused as a small endpoint")
	}
	_ = io.Discard
}

// An endpoint with nothing to give must be reported as SHORT, not as a slow path.
// youtube's probe target is a 204 — no body at all — so its throughput canary
// scored 0 KiB/s against every strategy ever applied and demoted all of them. The
// number was about the URL, never about the path.
func TestProbeMarksAnEndpointThatRanOutShort(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent) // 204: no body, by definition
	}))
	defer empty.Close()

	q := Probe(context.Background(), empty.Client(), []string{empty.URL}, 64<<10, 1)
	if !q.Short {
		t.Fatalf("a 204 must be Short — nothing was measured about the path; got %+v", q)
	}

	tiny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 300)) // robots.txt-sized, like the x service's target
	}))
	defer tiny.Close()
	if q := Probe(context.Background(), tiny.Client(), []string{tiny.URL}, 64<<10, 1); !q.Short {
		t.Errorf("a 300-byte endpoint must be Short, got %+v", q)
	}
}

// A path that DELIVERS the volume asked for is not short, and its goodput is a
// real measurement.
func TestProbeDoesNotMarkAFullReadShort(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 128<<10))
	}))
	defer big.Close()

	q := Probe(context.Background(), big.Client(), []string{big.URL}, 64<<10, 1)
	if q.Short {
		t.Errorf("a full read must not be Short: %+v", q)
	}
	if q.GoodputKBps <= 0 {
		t.Errorf("a full read must report goodput: %+v", q)
	}
}
