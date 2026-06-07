package noderank

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/dataplane"
)

// fakeAPI is an in-memory Clash stand-in. delays[node][url] = latency ms; a
// missing entry or 0 means that probe fails (node dead for that URL).
//
// The screen URL (defaultScreenURL) models a generic liveness check: a node is
// reachable for the screen iff it has ANY non-zero delay entry. This mirrors
// real life — a live exit answers a generate_204, a dead one answers nothing —
// without forcing every test to add an explicit screen-URL entry per node.
type fakeAPI struct {
	mu         sync.Mutex
	info       dataplane.ProxyInfo
	delays     map[string]map[string]int
	setCalls   []string       // targets passed to SetSelector, in order
	probeCalls map[string]int // node -> count of FULL (non-screen) NodeDelay calls
}

func (f *fakeAPI) Proxy(_ context.Context, _ string) (dataplane.ProxyInfo, error) {
	return f.info, nil
}

func (f *fakeAPI) NodeDelay(_ context.Context, name, testURL string, _ time.Duration) (int, error) {
	if testURL == defaultScreenURL {
		// Screen phase: alive iff the node has any non-zero delay entry.
		f.mu.Lock()
		m := f.delays[name]
		f.mu.Unlock()
		for _, d := range m {
			if d > 0 {
				return d, nil
			}
		}
		return 0, fmt.Errorf("fake: %s dead on screen", name)
	}
	f.mu.Lock()
	if f.probeCalls == nil {
		f.probeCalls = map[string]int{}
	}
	f.probeCalls[name]++
	m, ok := f.delays[name]
	f.mu.Unlock()
	if ok {
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

// Switch margin (anti-flap): once a node is advised, a marginally-better node
// must NOT flip the advice; only a clearly-better one (beating the advised node
// by more than SwitchMargin) wins. Stickiness keeps the service from ping-ponging
// between two near-equal exits every cycle.
func TestSwitchMarginKeepsStickyNode(t *testing.T) {
	api := newFake("direct", []string{"a", "b", "direct"},
		map[string]map[string]int{"a": {svcURL: 30}, "b": {svcURL: 90}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "x", Selector: "sel-x", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{"a", ""}, {"b", ""}}

	// Cycle 1: a (30ms) clearly beats b (90ms) -> a is advised.
	if got, _ := r.Pick(context.Background(), svc, cands); got != "a" {
		t.Fatalf("cycle1: got %q, want a", got)
	}
	// Cycle 2: b improves to be only MARGINALLY better than a. The score delta
	// stays under SwitchMargin, so advice must STAY on a (no flap).
	api.delays = map[string]map[string]int{"a": {svcURL: 30}, "b": {svcURL: 28}}
	if got, _ := r.Pick(context.Background(), svc, cands); got != "a" {
		t.Fatalf("marginal improvement must not flip advice, got %q, want a (sticky)", got)
	}
	// Cycle 3: b becomes CLEARLY better (much lower latency) -> exceeds margin,
	// advice flips to b.
	api.delays = map[string]map[string]int{"a": {svcURL: 200}, "b": {svcURL: 10}}
	if got, _ := r.Pick(context.Background(), svc, cands); got != "b" {
		t.Fatalf("clear improvement must flip advice, got %q, want b", got)
	}
}

// Switch margin only guards healthy-vs-healthy churn: if the currently-advised
// node goes DOWN, advice switches to the best survivor regardless of margin.
func TestSwitchMarginIgnoredWhenAdvisedDown(t *testing.T) {
	api := newFake("direct", []string{"a", "b", "direct"},
		map[string]map[string]int{"a": {svcURL: 30}, "b": {svcURL: 90}})
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "x", Selector: "sel-x", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{"a", ""}, {"b", ""}}

	// Cycle 1: a advised.
	if got, _ := r.Pick(context.Background(), svc, cands); got != "a" {
		t.Fatalf("cycle1: got %q, want a", got)
	}
	// a goes dead; downAfter=3 -> three failing cycles to evict it. b stays the
	// only healthy node throughout. Even while a is merely degraded a healthy b
	// outranks it, and once a is DOWN the margin cannot keep it.
	api.delays = map[string]map[string]int{"b": {svcURL: 90}}
	for i := 1; i <= 3; i++ {
		if got, _ := r.Pick(context.Background(), svc, cands); got != "b" {
			t.Fatalf("cycle %d: advised node down/degraded, must switch to b, got %q", i, got)
		}
	}
}

// 2-phase validation: a node that is dead on the cheap screen is dropped BEFORE
// the full per-service probe runs, so the expensive probe is never spent on it.
// We assert this via the fake's full-probe call counter.
func TestScreenSkipsFullProbeOnDeadNode(t *testing.T) {
	api := newFake("direct", []string{"live", "dead", "direct"},
		map[string]map[string]int{
			"live": {svcURL: 20}, // answers screen + full probe
			"dead": {},           // no entries -> fails the screen
		})
	r := New(api, 2, false, quietLog()) // samples=2: full probe would be 2 calls if it ran
	svc := Service{Name: "x", Selector: "sel-x", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	cands := []Candidate{{"live", ""}, {"dead", ""}}

	got, _ := r.Pick(context.Background(), svc, cands)
	if got != "live" {
		t.Fatalf("got %q, want live (dead screened out)", got)
	}
	if n := api.probeCalls["dead"]; n != 0 {
		t.Fatalf("dead node screened out but full probe ran %d times, want 0", n)
	}
	if n := api.probeCalls["live"]; n != r.samples {
		t.Fatalf("live node full probe ran %d times, want %d", n, r.samples)
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

// LOT-12: a sticky service keeps its healthy advised node even when a far better
// candidate appears (never flip by latency); a non-sticky service switches.
func TestStickyKeepsHealthyNodeDespiteBetterCandidate(t *testing.T) {
	run := func(sticky bool) (first, second string) {
		api := newFake("a", []string{"a", "b", "direct"},
			map[string]map[string]int{"a": {svcURL: 10}, "b": {svcURL: 90}})
		r := New(api, 1, false, quietLog())
		svc := Service{Name: "voice", Selector: "sel-voice", ProbeURL: svcURL,
			Weights: balancer.ProfileFor("voice"), Sticky: sticky}
		cands := []Candidate{{"a", ""}, {"b", ""}}
		first, _ = r.Pick(context.Background(), svc, cands) // advises a (best)
		// b becomes far better than a — well beyond the switch margin.
		api.delays = map[string]map[string]int{"a": {svcURL: 200}, "b": {svcURL: 5}}
		second, _ = r.Pick(context.Background(), svc, cands)
		return first, second
	}
	if f, s := run(false); f != "a" || s != "b" {
		t.Errorf("non-sticky: first=%q second=%q, want a then b (switches to far-better node)", f, s)
	}
	if f, s := run(true); f != "a" || s != "a" {
		t.Errorf("sticky: first=%q second=%q, want a then a (must not flip by latency)", f, s)
	}
}

// LOT-6: HealthSnapshot surfaces the ranker's own per-node health (healthy /
// degraded / down) — the visibility gap, no duplicate Tracker.
func TestHealthSnapshot(t *testing.T) {
	api := newFake("a", []string{"a", "b", "direct"},
		map[string]map[string]int{"a": {svcURL: 10}}) // a serves the svc URL; b is dead on it
	r := New(api, 1, false, quietLog())
	svc := Service{Name: "x", Selector: "sel-x", ProbeURL: svcURL, Weights: balancer.ProfileFor("general")}
	r.Pick(context.Background(), svc, []Candidate{{"a", ""}, {"b", ""}})

	state := map[string]string{}
	for _, h := range r.HealthSnapshot() {
		state[h.Node] = h.State
	}
	if state["a"] != "healthy" {
		t.Errorf("a = %q, want healthy", state["a"])
	}
	if state["b"] != "down" {
		t.Errorf("b = %q, want down (dead on the service URL, never good)", state["b"])
	}
}
