package desyncgen

import (
	"sort"
	"strings"
	"testing"
)

// fakeEngine is a tiny 3-axis engine for testing the generator core.
type fakeEngine struct{ axes []Axis }

func (f fakeEngine) Name() string { return "fake" }
func (f fakeEngine) Axes() []Axis { return f.axes }
func (f fakeEngine) Render(s Strategy) []string {
	var out []string
	for _, ax := range f.axes {
		if v, ok := s[ax.Name]; ok {
			out = append(out, ax.Name+"="+v)
		}
	}
	sort.Strings(out)
	return out
}

func eng() fakeEngine {
	return fakeEngine{axes: []Axis{
		{Name: "method", Kind: Categorical, Values: []string{"fake", "split2", "multisplit"}},
		{Name: "pos", Kind: Ordinal, Values: []string{"1", "2", "sni"}},
		{Name: "ttl", Kind: Numeric, Values: []string{"3", "5"}},
	}}
}

func TestGridCartesian(t *testing.T) {
	g := Grid(eng(), 0)
	if len(g) != 3*3*2 {
		t.Fatalf("grid size = %d, want 18", len(g))
	}
	// Every combination is unique and complete (3 axes set).
	seen := map[string]bool{}
	for _, s := range g {
		if len(s) != 3 {
			t.Fatalf("strategy missing axes: %v", s)
		}
		key := s["method"] + "|" + s["pos"] + "|" + s["ttl"]
		if seen[key] {
			t.Errorf("duplicate combination %s", key)
		}
		seen[key] = true
	}
}

func TestGridCap(t *testing.T) {
	if g := Grid(eng(), 5); len(g) != 5 {
		t.Errorf("capped grid = %d, want 5", len(g))
	}
}

// A capped grid must SAMPLE the space, not collapse onto the first axis's first
// values: the old per-axis truncation (next[:cap]) dropped whole high-order axis
// values (e.g. only method=fake survived), so the search never saw split2/
// multisplit. Coverage matters more than which 5 points — assert every value of
// every axis is still represented when the cap allows it.
func TestGridCapCoversEveryAxisValue(t *testing.T) {
	g := Grid(eng(), 6) // 6 >= max axis cardinality (3), so full coverage is possible
	if len(g) != 6 {
		t.Fatalf("capped grid = %d, want 6", len(g))
	}
	for _, ax := range eng().Axes() {
		for _, want := range ax.Values {
			seen := false
			for _, s := range g {
				if s[ax.Name] == want {
					seen = true
					break
				}
			}
			if !seen {
				t.Errorf("capped grid never sampled %s=%s (axis collapsed)", ax.Name, want)
			}
		}
	}
}

// Determinism: same engine + cap → identical sample (the KB tracks strategies by
// id, so the sweep must be reproducible across runs).
func TestGridCapDeterministic(t *testing.T) {
	a, b := Grid(eng(), 7), Grid(eng(), 7)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic length %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i]["method"]+a[i]["pos"]+a[i]["ttl"] != b[i]["method"]+b[i]["pos"]+b[i]["ttl"] {
			t.Errorf("sample %d differs between runs: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestGridSkipsEmptyAxis(t *testing.T) {
	e := fakeEngine{axes: []Axis{
		{Name: "method", Kind: Categorical, Values: []string{"fake", "split2"}},
		{Name: "empty", Kind: Categorical, Values: nil}, // does not vary
	}}
	g := Grid(e, 0)
	if len(g) != 2 {
		t.Fatalf("empty axis should not multiply: got %d, want 2", len(g))
	}
	if _, ok := g[0]["empty"]; ok {
		t.Error("empty axis must not appear in strategies")
	}
}

func TestMutateOneAxisNeighbours(t *testing.T) {
	seed := Strategy{"method": "fake", "pos": "2", "ttl": "3"}
	got := Mutate(eng(), seed)

	// Every neighbour differs from seed in EXACTLY one axis.
	for _, s := range got {
		diff := 0
		for _, k := range []string{"method", "pos", "ttl"} {
			if s[k] != seed[k] {
				diff++
			}
		}
		if diff != 1 {
			t.Errorf("neighbour %v differs from seed in %d axes, want 1", s, diff)
		}
	}

	// Categorical method: the 2 OTHER values (split2, multisplit).
	// Ordinal pos at index 1 ("2"): neighbours "1" and "sni" (index-adjacent).
	// Numeric ttl at index 0 ("3"): neighbour "5" only (no left).
	methods, poss, ttls := neighborsOf(got, seed, "method"), neighborsOf(got, seed, "pos"), neighborsOf(got, seed, "ttl")
	if strings.Join(methods, ",") != "multisplit,split2" {
		t.Errorf("method neighbours = %v, want [multisplit split2]", methods)
	}
	if strings.Join(poss, ",") != "1,sni" {
		t.Errorf("pos neighbours = %v, want [1 sni]", poss)
	}
	if strings.Join(ttls, ",") != "5" {
		t.Errorf("ttl neighbours = %v, want [5] (no left neighbour)", ttls)
	}
}

func TestMutateUnknownCurrentOffersAll(t *testing.T) {
	// seed missing 'ttl' -> all ttl values are reachable neighbours.
	seed := Strategy{"method": "fake", "pos": "1"}
	got := Mutate(eng(), seed)
	if ttls := neighborsOf(got, seed, "ttl"); strings.Join(ttls, ",") != "3,5" {
		t.Errorf("absent ordinal axis should offer all values, got %v", ttls)
	}
}

// MutateN radius 2 must contain points that change TWO axes at once — the ones a
// radius-1 climb can never reach when they sit across a conjunctive valley.
func TestMutateRadius2CrossesValley(t *testing.T) {
	seed := Strategy{"method": "fake", "pos": "2", "ttl": "3"}
	// A target two one-step moves from the seed: method->split2 AND ttl->5.
	target := Strategy{"method": "split2", "pos": "2", "ttl": "5"}
	key := func(s Strategy) string { return s["method"] + "|" + s["pos"] + "|" + s["ttl"] }

	r1 := map[string]bool{}
	for _, s := range Mutate(eng(), seed) {
		r1[key(s)] = true
	}
	if r1[key(target)] {
		t.Fatal("precondition: radius-1 should NOT reach a 2-axis target")
	}

	r2 := map[string]bool{}
	for _, s := range MutateN(eng(), seed, 2) {
		r2[key(s)] = true
	}
	if !r2[key(target)] {
		t.Error("radius-2 must reach the 2-axis target (split2,2,5)")
	}
	// radius 2 is a superset of radius 1.
	for k := range r1 {
		if !r2[k] {
			t.Errorf("radius-2 missing radius-1 neighbour %s", k)
		}
	}
}

func TestRenderContract(t *testing.T) {
	if got := eng().Render(Strategy{"method": "multisplit", "ttl": "5"}); strings.Join(got, " ") != "method=multisplit ttl=5" {
		t.Errorf("render = %v", got)
	}
}

// neighborsOf returns the sorted distinct values the mutated axis took (where the
// neighbour changed exactly that axis vs seed).
func neighborsOf(got []Strategy, seed Strategy, axis string) []string {
	set := map[string]bool{}
	for _, s := range got {
		if s[axis] != seed[axis] {
			// confirm only this axis changed
			only := true
			for k, v := range s {
				if k != axis && v != seed[k] {
					only = false
				}
			}
			if only {
				set[s[axis]] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
