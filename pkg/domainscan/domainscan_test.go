package domainscan

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestProbeAllOrderAndConcurrency(t *testing.T) {
	// fake probe: even-index domains OK, odd blocked.
	fake := func(_ context.Context, d string) Result {
		switch d {
		case "ok1.com", "ok2.com":
			return Result{Domain: d, OK: true, Status: 200, RTTms: 10}
		default:
			return Result{Domain: d, OK: false, Err: "timeout"}
		}
	}
	domains := []string{"ok1.com", "bad1.com", "ok2.com", "bad2.com"}
	got := ProbeAll(context.Background(), domains, fake, 4)
	// results preserve input order despite concurrency.
	if len(got) != 4 || got[0].Domain != "ok1.com" || got[3].Domain != "bad2.com" {
		t.Fatalf("order not preserved: %+v", got)
	}
	if !got[0].OK || got[1].OK {
		t.Errorf("OK flags wrong: %+v", got)
	}
}

func TestBlockedFiltersAndSorts(t *testing.T) {
	results := []Result{
		{Domain: "z.com", OK: true},
		{Domain: "b.com", OK: false, Err: "read tcp: connection reset by peer"},
		{Domain: "a.com", OK: false, Err: "context deadline exceeded"},
		{Domain: "d.com", OK: false, Err: "tls: failed to verify certificate"}, // artifact, NOT blocked
		{Domain: "c.com", OK: true},
	}
	got := Blocked(results)
	want := []string{"a.com", "b.com"} // timeout+reset only; cert artifact excluded; sorted
	var names []string
	for _, r := range got {
		names = append(names, r.Domain)
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Blocked = %v, want %v", names, want)
	}
}

// TestHTTP3ProbeLive hits a real H3 server to confirm the quic-go transport is
// wired correctly. Skipped unless LIVE_NET is set (keeps the suite offline/stable).
func TestHTTP3ProbeLive(t *testing.T) {
	if os.Getenv("LIVE_NET") == "" {
		t.Skip("set LIVE_NET=1 to run the live QUIC probe")
	}
	r := HTTP3Probe(8*time.Second)(context.Background(), "cloudflare.com")
	if !r.OK {
		t.Fatalf("HTTP3 probe to cloudflare.com failed: %s", r.Err)
	}
	t.Logf("QUIC ok: status=%d rtt=%dms", r.Status, r.RTTms)
}

func TestClassify(t *testing.T) {
	cases := []struct {
		r    Result
		want Verdict
	}{
		{Result{OK: true, Status: 403}, VerdictOK},                                 // any response = ok
		{Result{Err: "context deadline exceeded"}, VerdictBlocked},                 // timeout = block
		{Result{Err: "read: connection reset by peer"}, VerdictBlocked},            // RST = block
		{Result{Err: "timeout: no recent network activity"}, VerdictBlocked},       // quic idle = block
		{Result{Err: "tls: failed to verify certificate"}, VerdictInconclusive},    // cert artifact
		{Result{Err: "dial tcp: lookup x.com: no such host"}, VerdictInconclusive}, // no apex DNS
		{Result{Err: "CRYPTO_ERROR 0x12a"}, VerdictInconclusive},                   // reached server
	}
	for _, c := range cases {
		if got := Classify(c.r); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.r.Err, got, c.want)
		}
	}
}
