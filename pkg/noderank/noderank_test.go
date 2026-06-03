package noderank

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/dataplane"
)

// fakeAPI is an in-memory Clash stand-in. delays[node][url] = latency ms; a
// missing entry or 0 means that probe fails (node dead for that URL).
type fakeAPI struct {
	info     dataplane.ProxyInfo
	delays   map[string]map[string]int
	setCalls []string // targets passed to SetSelector, in order
}

func (f *fakeAPI) Proxy(_ context.Context, _ string) (dataplane.ProxyInfo, error) {
	return f.info, nil
}

func (f *fakeAPI) NodeDelay(_ context.Context, name, testURL string, _ time.Duration) (int, error) {
	if m, ok := f.delays[name]; ok {
		if d, ok := m[testURL]; ok && d > 0 {
			return d, nil
		}
	}
	return 0, fmt.Errorf("fake: %s dead for %s", name, testURL)
}

func (f *fakeAPI) SetSelector(_ context.Context, _, target string) error {
	f.setCalls = append(f.setCalls, target)
	f.info.Now = target
	return nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const svcURL = "https://www.youtube.com/generate_204"

func newFake(now string, members []string, delays map[string]map[string]int) *fakeAPI {
	return &fakeAPI{
		info:   dataplane.ProxyInfo{Type: "Selector", Now: now, All: members},
		delays: delays,
	}
}

// The headline test: a RU node is the fastest, but it must NOT be pinned for a
// blocked service. The ranker excludes it before probing and pins the best
// non-RU node. This is exactly the Hiddify/url-test failure we replace.
func TestExcludesRUEvenWhenFastest(t *testing.T) {
	api := newFake("ru-node", []string{"vpn_url_test", "ru-node", "de-node", "nl-node", "direct"},
		map[string]map[string]int{
			"ru-node": {svcURL: 5},  // fastest by ping...
			"de-node": {svcURL: 40}, // ...but RU is behind the same TSPU, so skip it
			"nl-node": {svcURL: 60},
		})
	r := New(api, 1, false, quietLog())
	svc := Service{
		Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL,
		Weights: balancer.ProfileFor("streaming"), ExcludeCC: []string{"ru"},
	}
	cands := []Candidate{{"ru-node", "ru"}, {"de-node", "de"}, {"nl-node", "nl"}}

	got, err := r.Pick(context.Background(), svc, cands)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got != "de-node" {
		t.Fatalf("recommended %q, want de-node (ru excluded despite lowest ping)", got)
	}
	// Advisory: the ranker records the recommendation, it never writes a selector.
	if best := r.Best(svc.Name); best != "de-node" {
		t.Fatalf("Best(%s) = %q, want de-node", svc.Name, best)
	}
	if len(api.setCalls) != 0 {
		t.Fatalf("ranker must not write any selector, got SetSelector calls %v", api.setCalls)
	}
}

// Probing is service-aware: a node that is alive on a generic URL but dead on
// the SERVICE URL must lose. We only ever probe through svc.ProbeURL, so a node
// with no svc-URL entry fails and the alive-on-svc node wins.
func TestServiceAwareProbeBeatsGenericPing(t *testing.T) {
	const generic = "https://www.gstatic.com/generate_204"
	api := newFake("direct", []string{"a", "b", "direct"},
		map[string]map[string]int{
			"a": {generic: 3}, // brilliant on generate_204, but YouTube path is dead
			"b": {svcURL: 80}, // mediocre ping, but actually serves the service
		})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{"a", "de"}, {"b", "nl"}}

	got, _ := r.Pick(context.Background(), svc, cands)
	if got != "b" {
		t.Fatalf("pinned %q, want b (a is dead on the service URL)", got)
	}
}

// IncludeCC is a hard allowlist: only listed countries pass, and unknown-country
// ("smart location") fails an include filter (cannot prove it's in-country).
func TestIncludeCountryAllowlist(t *testing.T) {
	api := newFake("direct", []string{"us1", "de1", "smart", "direct"},
		map[string]map[string]int{
			"us1":   {svcURL: 90},
			"de1":   {svcURL: 10}, // faster, but not US -> filtered out
			"smart": {svcURL: 8},  // fastest, but unknown country -> fails include
		})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "netflix", Selector: "sel-netflix", ProbeURL: svcURL,
		Weights: balancer.ProfileFor("streaming"), IncludeCC: []string{"us"}}
	cands := []Candidate{{"us1", "us"}, {"de1", "de"}, {"smart", ""}}

	got, _ := r.Pick(context.Background(), svc, cands)
	if got != "us1" {
		t.Fatalf("pinned %q, want us1 (include=[us] excludes de and unknown)", got)
	}
}

// Unknown-country node is never excluded by ExcludeCC and remains pinnable.
func TestUnknownCountryNotExcluded(t *testing.T) {
	api := newFake("direct", []string{"smart", "direct"},
		map[string]map[string]int{"smart": {svcURL: 20}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "ai", Selector: "sel-ai", ProbeURL: svcURL,
		Weights: balancer.ProfileFor("general"), ExcludeCC: []string{"ru"}}
	got, _ := r.Pick(context.Background(), svc, []Candidate{{"smart", ""}})
	if got != "smart" {
		t.Fatalf("pinned %q, want smart (unknown country must not be excluded)", got)
	}
}

// dry-run probes but never switches.
func TestDryRunDoesNotPin(t *testing.T) {
	api := newFake("nl-node", []string{"de-node", "nl-node", "direct"},
		map[string]map[string]int{"de-node": {svcURL: 10}, "nl-node": {svcURL: 90}})
	r := New(api, 1, true, quietLog())
	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	got, _ := r.Pick(context.Background(), svc, []Candidate{{"de-node", "de"}, {"nl-node", "nl"}})
	if got != "de-node" {
		t.Fatalf("dry-run chose %q, want de-node", got)
	}
	if len(api.setCalls) != 0 {
		t.Fatalf("dry-run must not call SetSelector, got %v", api.setCalls)
	}
}

// Already on the best node: no redundant switch.
func TestNoSwitchWhenBestAlreadyActive(t *testing.T) {
	api := newFake("de-node", []string{"de-node", "nl-node", "direct"},
		map[string]map[string]int{"de-node": {svcURL: 10}, "nl-node": {svcURL: 90}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	got, _ := r.Pick(context.Background(), svc, []Candidate{{"de-node", "de"}, {"nl-node", "nl"}})
	if got != "de-node" {
		t.Fatalf("got %q, want de-node", got)
	}
	if len(api.setCalls) != 0 {
		t.Fatalf("best already active: must not switch, got %v", api.setCalls)
	}
}

// All eligible nodes dead on the service URL -> nothing pinned, no switch.
func TestAllDownPinsNothing(t *testing.T) {
	api := newFake("de-node", []string{"de-node", "nl-node", "direct"},
		map[string]map[string]int{}) // no entries -> every probe fails
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	got, _ := r.Pick(context.Background(), svc, []Candidate{{"de-node", "de"}, {"nl-node", "nl"}})
	if got != "" {
		t.Fatalf("got %q, want empty (all down)", got)
	}
	if len(api.setCalls) != 0 {
		t.Fatalf("all down: must not switch, got %v", api.setCalls)
	}
}

// Hysteresis: a node that was healthy and then fails ONE probe is kept (degraded),
// not evicted; only sustained failure (downAfter in a row) drops it. This stops a
// transient blip from throwing away a good/pinned node.
func TestHysteresisKeepsBlippingNode(t *testing.T) {
	api := newFake("a", []string{"a", "direct"}, map[string]map[string]int{"a": {svcURL: 20}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "x", Selector: "sel-x", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{"a", ""}}

	if got, _ := r.Pick(context.Background(), svc, cands); got != "a" {
		t.Fatalf("cycle1 (healthy): got %q, want a", got)
	}
	// Node a now fails every probe.
	api.delays = map[string]map[string]int{}
	// downAfter is 3: the first two failures are "degraded" -> a is kept.
	for i := 1; i <= 2; i++ {
		if got, _ := r.Pick(context.Background(), svc, cands); got != "a" {
			t.Fatalf("blip %d: degraded node must be kept, got %q", i, got)
		}
	}
	// Third consecutive failure -> down -> evicted.
	if got, _ := r.Pick(context.Background(), svc, cands); got != "" {
		t.Fatalf("after %d consecutive failures node must be evicted, got %q", 3, got)
	}
}

// A healthy node outranks a degraded one (we keep the blipping node usable but do
// not prefer it over a live alternative).
func TestHealthyBeatsDegraded(t *testing.T) {
	api := newFake("a", []string{"a", "b", "direct"},
		map[string]map[string]int{"a": {svcURL: 10}, "b": {svcURL: 90}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "x", Selector: "sel-x", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{"a", ""}, {"b", ""}}

	// Cycle1: both healthy, a faster -> a.
	if got, _ := r.Pick(context.Background(), svc, cands); got != "a" {
		t.Fatalf("cycle1: got %q, want a", got)
	}
	// a starts failing; b stays healthy. a is degraded but b (healthy) must win.
	api.delays = map[string]map[string]int{"b": {svcURL: 90}}
	if got, _ := r.Pick(context.Background(), svc, cands); got != "b" {
		t.Fatalf("degraded a must lose to healthy b, got %q", got)
	}
}

// Winner not listed in the selector -> cannot pin (would 400), returns empty.
func TestWinnerNotSelectorMember(t *testing.T) {
	api := newFake("direct", []string{"direct"}, // selector lists no concrete nodes
		map[string]map[string]int{"de-node": {svcURL: 10}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	got, _ := r.Pick(context.Background(), svc, []Candidate{{"de-node", "de"}})
	if got != "" {
		t.Fatalf("got %q, want empty (winner not a selector member)", got)
	}
	if len(api.setCalls) != 0 {
		t.Fatalf("must not pin a non-member, got %v", api.setCalls)
	}
}

// No country-eligible candidates -> nothing to do.
func TestNoEligibleCandidates(t *testing.T) {
	api := newFake("direct", []string{"ru-node", "direct"},
		map[string]map[string]int{"ru-node": {svcURL: 5}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "youtube", Selector: "sel-youtube", ProbeURL: svcURL,
		Weights: balancer.ProfileFor("general"), ExcludeCC: []string{"ru"}}
	got, _ := r.Pick(context.Background(), svc, []Candidate{{"ru-node", "ru"}})
	if got != "" {
		t.Fatalf("got %q, want empty (only candidate excluded by country)", got)
	}
	if len(api.setCalls) != 0 {
		t.Fatalf("must not switch with no eligible nodes, got %v", api.setCalls)
	}
}
