package iplearn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
)

// --- helpers ----------------------------------------------------------------

func mustNet(t *testing.T, s string) *net.IPNet {
	t.Helper()
	n := parseCIDROrIP(s)
	if n == nil {
		t.Fatalf("bad test CIDR %q", s)
	}
	return n
}

func cidrStrings(nets []*net.IPNet) []string {
	out := make([]string, len(nets))
	for i, n := range nets {
		out[i] = n.String()
	}
	return out
}

func contains(nets []*net.IPNet, cidr string) bool {
	for _, n := range nets {
		if n.String() == cidr {
			return true
		}
	}
	return false
}

// fakeHTTP returns a canned response (or error) regardless of request.
type fakeHTTP struct {
	body       string
	status     int
	err        error
	lastURL    string
	calledWith []string
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.lastURL = req.URL.String()
	f.calledWith = append(f.calledWith, req.URL.String())
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(f.body)),
		Header:     make(http.Header),
	}, nil
}

// --- Learner ----------------------------------------------------------------

func TestLearnerDedupAndSnapshot(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", net.ParseIP("1.2.3.4"))
	l.Observe("yt", net.ParseIP("1.2.3.4")) // duplicate
	l.Observe("yt", net.ParseIP("1.2.3.5"))
	l.Observe("yt", net.ParseIP("2001:db8::1"))

	snap := l.Snapshot("yt")
	if len(snap) != 3 {
		t.Fatalf("want 3 deduped CIDRs, got %d: %v", len(snap), cidrStrings(snap))
	}
	for _, want := range []string{"1.2.3.4/32", "1.2.3.5/32", "2001:db8::1/128"} {
		if !contains(snap, want) {
			t.Errorf("snapshot missing %s: %v", want, cidrStrings(snap))
		}
	}
}

func TestLearnerContainedIPDoesNotGrow(t *testing.T) {
	l := NewLearner()
	// Observe a broad block first, then a host inside it.
	l.ObserveCIDR("yt", mustNet(t, "10.0.0.0/8"))
	before := l.Snapshot("yt")
	l.Observe("yt", net.ParseIP("10.1.2.3")) // contained in 10.0.0.0/8
	after := l.Snapshot("yt")

	if len(after) != len(before) {
		t.Fatalf("contained IP grew the set: before=%v after=%v",
			cidrStrings(before), cidrStrings(after))
	}
	if !contains(after, "10.0.0.0/8") {
		t.Fatalf("broad block lost: %v", cidrStrings(after))
	}
}

func TestLearnerBroadEvictsNarrow(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", net.ParseIP("10.1.2.3")) // /32
	l.ObserveCIDR("yt", mustNet(t, "10.0.0.0/8"))
	snap := l.Snapshot("yt")
	if len(snap) != 1 || !contains(snap, "10.0.0.0/8") {
		t.Fatalf("broad block should evict contained /32, got %v", cidrStrings(snap))
	}
}

func TestLearnerServicesSeparate(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", net.ParseIP("1.2.3.4"))
	l.Observe("discord", net.ParseIP("5.6.7.8"))

	yt := l.Snapshot("yt")
	dc := l.Snapshot("discord")
	if len(yt) != 1 || !contains(yt, "1.2.3.4/32") {
		t.Errorf("yt set wrong: %v", cidrStrings(yt))
	}
	if len(dc) != 1 || !contains(dc, "5.6.7.8/32") {
		t.Errorf("discord set wrong: %v", cidrStrings(dc))
	}
	if svcs := l.Services(); len(svcs) != 2 {
		t.Errorf("want 2 services, got %v", svcs)
	}
}

func TestLearnerIgnoresNilAndUnspecified(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", nil)
	l.Observe("yt", net.IPv4zero)
	l.ObserveCIDR("yt", nil)
	if snap := l.Snapshot("yt"); len(snap) != 0 {
		t.Fatalf("nil/unspecified should be ignored, got %v", cidrStrings(snap))
	}
}

func TestLearnerCoalesce(t *testing.T) {
	l := NewLearner(WithCoalesce(true))
	// Two sibling /32s should coalesce up to a /31, then their sibling /31
	// neighbours into a /30, etc. Provide a full /30 worth of hosts.
	for _, ip := range []string{"192.0.2.0", "192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		l.Observe("yt", net.ParseIP(ip))
	}
	snap := l.Snapshot("yt")
	if len(snap) != 1 || !contains(snap, "192.0.2.0/30") {
		t.Fatalf("four hosts should coalesce to /30, got %v", cidrStrings(snap))
	}
}

// stubWidener widens any v4 host route to its /24.
type stubWidener struct{}

func (stubWidener) Widen(host *net.IPNet) *net.IPNet {
	if v4 := host.IP.To4(); v4 != nil {
		_, n, _ := net.ParseCIDR(fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2]))
		return n
	}
	return host
}

func TestLearnerWidener(t *testing.T) {
	l := NewLearner(WithWidener(stubWidener{}))
	l.Observe("yt", net.ParseIP("203.0.113.5"))
	l.Observe("yt", net.ParseIP("203.0.113.200")) // same /24
	snap := l.Snapshot("yt")
	if len(snap) != 1 || !contains(snap, "203.0.113.0/24") {
		t.Fatalf("widener should fold hosts into /24, got %v", cidrStrings(snap))
	}
}

func TestLearnerConcurrent(t *testing.T) {
	l := NewLearner()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Observe("yt", net.IPv4(10, 0, byte(i/256), byte(i%256)))
			_ = l.Snapshot("yt")
		}(i)
	}
	wg.Wait()
}

// --- RekrytSource -----------------------------------------------------------

func TestRekrytSourceFetchParses(t *testing.T) {
	body := "1.2.3.0/24\n# a comment\n\n5.6.7.8\n2001:db8::/32\nnot-a-cidr\n10.0.0.0/8 // inline\n"
	fake := &fakeHTTP{body: body}
	src := NewRekrytSource(fake)

	nets, err := src.Fetch(context.Background(), "googlevideo.com")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for _, want := range []string{"1.2.3.0/24", "5.6.7.8/32", "2001:db8::/32", "10.0.0.0/8"} {
		if !contains(nets, want) {
			t.Errorf("missing %s in %v", want, cidrStrings(nets))
		}
	}
	if len(nets) != 4 {
		t.Errorf("want 4 valid CIDRs (malformed skipped), got %d: %v", len(nets), cidrStrings(nets))
	}
	// Verify the query was built correctly.
	for _, frag := range []string{"format=text", "site=googlevideo.com", "data=cidr4"} {
		if !bytes.Contains([]byte(fake.lastURL), []byte(frag)) {
			t.Errorf("request URL %q missing %q", fake.lastURL, frag)
		}
	}
}

func TestRekrytSourceHTTPError(t *testing.T) {
	src := NewRekrytSource(&fakeHTTP{err: fmt.Errorf("boom")})
	if _, err := src.Fetch(context.Background(), "x.com"); err == nil {
		t.Fatal("expected error from failing HTTP client")
	}
}

func TestRekrytSourceStatusError(t *testing.T) {
	src := NewRekrytSource(&fakeHTTP{status: http.StatusInternalServerError, body: "1.2.3.4"})
	if _, err := src.Fetch(context.Background(), "x.com"); err == nil {
		t.Fatal("expected error on non-200 status")
	}
}

func TestRekrytSourceNoClient(t *testing.T) {
	src := &RekrytSource{}
	if _, err := src.Fetch(context.Background(), "x.com"); err == nil {
		t.Fatal("expected error when HTTP client is nil")
	}
}

// --- MergeSources -----------------------------------------------------------

func TestMergeSourcesUnion(t *testing.T) {
	external := []*net.IPNet{mustNet(t, "1.2.3.0/24"), mustNet(t, "5.5.5.5/32")}
	learned := []*net.IPNet{mustNet(t, "1.2.3.4/32"), mustNet(t, "9.9.9.9/32")} // /32 contained in /24

	res := MergeSources(external, learned, nil, 0.7)
	if res.Rejected {
		t.Fatal("first run should not be rejected")
	}
	// 1.2.3.4/32 is absorbed by 1.2.3.0/24 -> expect 3 entries.
	if len(res.CIDRs) != 3 {
		t.Fatalf("want 3 merged CIDRs, got %d: %v", len(res.CIDRs), cidrStrings(res.CIDRs))
	}
	if contains(res.CIDRs, "1.2.3.4/32") {
		t.Errorf("contained /32 should be absorbed: %v", cidrStrings(res.CIDRs))
	}
	for _, want := range []string{"1.2.3.0/24", "5.5.5.5/32", "9.9.9.9/32"} {
		if !contains(res.CIDRs, want) {
			t.Errorf("missing %s: %v", want, cidrStrings(res.CIDRs))
		}
	}
}

func TestMergeSourcesShrinkGuardRejects(t *testing.T) {
	lastGood := []*net.IPNet{
		mustNet(t, "1.0.0.0/32"), mustNet(t, "1.0.0.1/32"), mustNet(t, "1.0.0.2/32"),
		mustNet(t, "1.0.0.3/32"), mustNet(t, "1.0.0.4/32"), mustNet(t, "1.0.0.5/32"),
		mustNet(t, "1.0.0.6/32"), mustNet(t, "1.0.0.7/32"), mustNet(t, "1.0.0.8/32"),
		mustNet(t, "1.0.0.9/32"),
	}
	// Fresh union has only 2 entries — well below 70% of 10.
	external := []*net.IPNet{mustNet(t, "2.2.2.2/32")}
	learned := []*net.IPNet{mustNet(t, "3.3.3.3/32")}

	res := MergeSources(external, learned, lastGood, 0.7)
	if !res.Rejected {
		t.Fatalf("expected shrink guard to reject (fresh=%d, last=%d)", res.FreshCount, len(lastGood))
	}
	if len(res.CIDRs) != len(lastGood) {
		t.Fatalf("rejected merge should retain last-good (%d), got %d", len(lastGood), len(res.CIDRs))
	}
	if !contains(res.CIDRs, "1.0.0.5/32") {
		t.Errorf("last-good content not retained: %v", cidrStrings(res.CIDRs))
	}
}

func TestMergeSourcesShrinkGuardAccepts(t *testing.T) {
	lastGood := []*net.IPNet{
		mustNet(t, "1.0.0.0/32"), mustNet(t, "1.0.0.1/32"), mustNet(t, "1.0.0.2/32"),
		mustNet(t, "1.0.0.3/32"),
	}
	// Fresh has 3 of 4 = 75% >= 70%, accepted.
	external := []*net.IPNet{mustNet(t, "1.0.0.0/32"), mustNet(t, "1.0.0.1/32"), mustNet(t, "1.0.0.2/32")}
	res := MergeSources(external, nil, lastGood, 0.7)
	if res.Rejected {
		t.Fatalf("75%% retention should pass the guard (fresh=%d)", res.FreshCount)
	}
	if len(res.CIDRs) != 3 {
		t.Fatalf("want 3 fresh CIDRs, got %d", len(res.CIDRs))
	}
}

func TestMergeSourcesGuardDisabled(t *testing.T) {
	lastGood := []*net.IPNet{mustNet(t, "1.0.0.0/32"), mustNet(t, "1.0.0.1/32")}
	res := MergeSources(nil, []*net.IPNet{mustNet(t, "9.9.9.9/32")}, lastGood, 0)
	if res.Rejected {
		t.Fatal("ratio<=0 disables the guard")
	}
	if len(res.CIDRs) != 1 {
		t.Fatalf("want fresh set of 1, got %d", len(res.CIDRs))
	}
}

// --- BuildService -----------------------------------------------------------

func TestBuildServiceUnionsExternalAndLearned(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", net.ParseIP("9.9.9.9"))
	src := NewRekrytSource(&fakeHTTP{body: "1.2.3.0/24\n"})
	cfg := ServiceConfig{Name: "yt", Domain: "googlevideo.com", ExternalEnabled: true}

	res, err := BuildService(context.Background(), src, l, cfg, nil, 0.7)
	if err != nil {
		t.Fatalf("BuildService: %v", err)
	}
	if !contains(res.CIDRs, "1.2.3.0/24") || !contains(res.CIDRs, "9.9.9.9/32") {
		t.Fatalf("want external+learned union, got %v", cidrStrings(res.CIDRs))
	}
}

func TestBuildServiceExternalDisabledUsesLearnedOnly(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", net.ParseIP("9.9.9.9"))
	src := NewRekrytSource(&fakeHTTP{body: "1.2.3.0/24\n"})
	cfg := ServiceConfig{Name: "yt", Domain: "googlevideo.com", ExternalEnabled: false}

	res, err := BuildService(context.Background(), src, l, cfg, nil, 0.7)
	if err != nil {
		t.Fatalf("BuildService: %v", err)
	}
	if contains(res.CIDRs, "1.2.3.0/24") {
		t.Errorf("external should be skipped when disabled: %v", cidrStrings(res.CIDRs))
	}
	if !contains(res.CIDRs, "9.9.9.9/32") {
		t.Errorf("learned set missing: %v", cidrStrings(res.CIDRs))
	}
}

func TestBuildServiceFetchErrorStillMergesLearned(t *testing.T) {
	l := NewLearner()
	l.Observe("yt", net.ParseIP("9.9.9.9"))
	src := NewRekrytSource(&fakeHTTP{err: fmt.Errorf("network down")})
	cfg := ServiceConfig{Name: "yt", Domain: "googlevideo.com", ExternalEnabled: true}

	res, err := BuildService(context.Background(), src, l, cfg, nil, 0.7)
	if err == nil {
		t.Fatal("expected fetch error surfaced")
	}
	if !contains(res.CIDRs, "9.9.9.9/32") {
		t.Fatalf("learned set should survive a failed fetch: %v", cidrStrings(res.CIDRs))
	}
}

// --- Coalesce ---------------------------------------------------------------

func TestCoalesceNoSiblingNoMerge(t *testing.T) {
	in := []*net.IPNet{mustNet(t, "192.0.2.0/32"), mustNet(t, "192.0.2.5/32")}
	out := Coalesce(in)
	if len(out) != 2 {
		t.Fatalf("non-sibling /32s must not merge, got %v", cidrStrings(out))
	}
}

func TestCoalesceMixedFamilies(t *testing.T) {
	in := []*net.IPNet{
		mustNet(t, "192.0.2.0/32"), mustNet(t, "192.0.2.1/32"),
		mustNet(t, "2001:db8::/64"),
	}
	out := Coalesce(in)
	if !contains(out, "192.0.2.0/31") {
		t.Errorf("v4 siblings should merge to /31: %v", cidrStrings(out))
	}
	if !contains(out, "2001:db8::/64") {
		t.Errorf("v6 entry lost: %v", cidrStrings(out))
	}
}
