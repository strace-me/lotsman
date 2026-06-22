// Package desyncgen is the engine-agnostic core of the autonomous desync-strategy
// generator (LOT-31 / v7). It models an engine's DPI-desync parameter space as a
// set of AXES and produces candidate STRATEGIES two ways: Grid (cartesian sweep,
// cold start) and Mutate (one-axis neighbours of a working seed, hill-climb).
//
// It is deliberately pure and engine-agnostic: it knows nothing about nfqws,
// byedpi, youtube-unblock, etc. — only Axis/Strategy/Engine. A concrete engine
// supplies its axes (its parameter space) and a Render that turns a Strategy into
// that engine's arg tokens; the shared tuner loop (separate) then applies each
// candidate in isolation, probes it (pkg/tester), and keeps the winners. To add
// a new packet-mangling engine you write an Engine — the generation, probing, and
// ranking machinery is reused.
package desyncgen

// AxisKind classifies how an axis's neighbours are derived during Mutate.
type AxisKind int

const (
	// Categorical: unordered options (e.g. desync method, fooling). Every OTHER
	// value is a neighbour.
	Categorical AxisKind = iota
	// Ordinal: ordered options where adjacency is meaningful but spacing is not
	// (e.g. split-pos = 1,2,sni,midsld). Neighbours are the index-adjacent values.
	Ordinal
	// Numeric: ordered numbers (e.g. ttl, seqovl, repeats). Same neighbour rule as
	// Ordinal (index±1 over the supplied candidate values).
	Numeric
)

// Axis is one tunable dimension of an engine's desync parameter space. Values are
// the discrete candidates to consider; for Ordinal/Numeric their ORDER defines
// adjacency (Mutate steps to index±1). An axis with no Values does not vary.
type Axis struct {
	Name   string
	Kind   AxisKind
	Values []string
}

// Strategy is one point in the space: a chosen value per axis name. An engine's
// Render turns it into concrete arg tokens.
type Strategy map[string]string

// Engine is a concrete packet-mangling engine's profile: its parameter space
// (Axes) and how to render a Strategy into its arg tokens. Render is what the
// tuner feeds to the engine's (isolated) apply.
type Engine interface {
	Name() string
	Axes() []Axis
	Render(Strategy) []string
}

// Seeder is an OPTIONAL engine capability: a catalog of known-good strategies
// (hand-tuned community/live recipes decomposed into this engine's axes). When an
// engine implements it, the tuner starts a COLD search from these priors instead
// of a blind Grid — the human recipe is the strong prior a blind sweep can't
// rediscover in a flat, deceptive landscape.
type Seeder interface {
	Seeds() []Strategy
}

// RawSeeder is an OPTIONAL engine capability: a catalog of FIXED-POINT recipes —
// full pre-baked arg strings from a community library (e.g. SlenderSolo's
// strategies.txt). Unlike Seeds (axis points the search Mutates around), these are
// tested AS-IS and never mutated: they carry flag combinations the axis model may
// not fully express, so they widen the cold-start candidate set with proven points
// the search can then explore around.
type RawSeeder interface {
	RawSeeds() []Strategy
}

// Grid returns candidate strategies sampled from the cartesian product of every
// non-empty axis's Values. With cap <= 0 it returns the full product. With cap > 0
// and a product larger than cap it returns cap strategies sampled EVENLY across
// the product's index space (a deterministic, low-discrepancy bound against the
// combinatorial blow-up) — NOT the first cap combinations, which would collapse
// onto the first axis's leading values and hide whole techniques from the search.
// The empty strategy is returned when there are no varying axes.
func Grid(e Engine, cap int) []Strategy {
	axes := make([]Axis, 0, len(e.Axes()))
	total := 1
	for _, ax := range e.Axes() {
		if len(ax.Values) == 0 {
			continue
		}
		axes = append(axes, ax)
		total *= len(ax.Values)
	}
	if len(axes) == 0 {
		return []Strategy{{}}
	}

	// Full product, or cap indices sampled low-discrepancy across [0,total). A
	// plain stride (k*total/n) resonates with the axis periods and can starve an
	// axis (a stride of 3 over a radix-3 axis only ever hits one value); a Weyl
	// sequence with a step coprime to total avoids that and spreads evenly.
	if cap <= 0 || cap >= total {
		out := make([]Strategy, 0, total)
		for k := 0; k < total; k++ {
			out = append(out, decode(axes, k))
		}
		return out
	}
	step := weylStep(total)
	out := make([]Strategy, 0, cap)
	for k := 0; k < cap; k++ {
		out = append(out, decode(axes, (k*step)%total))
	}
	return out
}

// weylStep picks a stride coprime to total near total/φ (the golden ratio), so
// {k*step mod total} is a low-discrepancy permutation of the product indices.
func weylStep(total int) int {
	step := int(float64(total) * 0.6180339887)
	if step < 1 {
		step = 1
	}
	for gcd(step, total) != 1 {
		step++
		if step >= total {
			return 1 // coprime to everything; total is tiny
		}
	}
	return step
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// decode turns a flat product index into a Strategy via mixed-radix over axes
// (the last axis varies fastest), so consecutive indices differ minimally and an
// evenly-strided sample covers every axis.
func decode(axes []Axis, idx int) Strategy {
	s := make(Strategy, len(axes))
	for i := len(axes) - 1; i >= 0; i-- {
		vals := axes[i].Values
		s[axes[i].Name] = vals[idx%len(vals)]
		idx /= len(vals)
	}
	return s
}

// Mutate returns the one-axis neighbourhood of seed: for each axis, the candidate
// strategies that differ from seed in EXACTLY that axis. Categorical axes yield
// every other value; Ordinal/Numeric axes yield the index-adjacent values (so a
// hill-climb steps one notch at a time). If seed has no value for an axis (or a
// value not in its candidates), every candidate value is offered. Duplicates are
// not produced (each neighbour changes a distinct axis to a distinct value).
func Mutate(e Engine, seed Strategy) []Strategy {
	var out []Strategy
	for _, ax := range e.Axes() {
		if len(ax.Values) == 0 {
			continue
		}
		cur, has := seed[ax.Name]
		idx := indexOf(ax.Values, cur)
		for _, v := range neighbourValues(ax, cur, has, idx) {
			s := clone(seed)
			s[ax.Name] = v
			out = append(out, s)
		}
	}
	return out
}

// MutateN returns the neighbourhood of seed within Hamming radius `radius` over
// the axes: every strategy that changes 1..radius DISTINCT axes, each to one of
// that axis's one-step neighbour values. radius<=1 is exactly Mutate. Radius 2 is
// what lets a one-shot A/B escape a CONJUNCTIVE valley — an optimum two axes away
// from the seed where every single-axis step looks worse, so a radius-1 climb
// stalls. Results are de-duplicated; the seed itself is never returned.
func MutateN(e Engine, seed Strategy, radius int) []Strategy {
	if radius <= 1 {
		return Mutate(e, seed)
	}
	type move struct{ axis, val string }
	var perAxis [][]move
	for _, ax := range e.Axes() {
		if len(ax.Values) == 0 {
			continue
		}
		cur, has := seed[ax.Name]
		var ms []move
		for _, v := range neighbourValues(ax, cur, has, indexOf(ax.Values, cur)) {
			ms = append(ms, move{ax.Name, v})
		}
		if len(ms) > 0 {
			perAxis = append(perAxis, ms)
		}
	}
	var out []Strategy
	seen := map[string]bool{}
	var rec func(start int, acc []move)
	rec = func(start int, acc []move) {
		if len(acc) > 0 {
			s := clone(seed)
			key := ""
			for _, m := range acc {
				s[m.axis] = m.val
				key += m.axis + "=" + m.val + ";"
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, s)
			}
		}
		if len(acc) == radius {
			return
		}
		for i := start; i < len(perAxis); i++ {
			for _, m := range perAxis[i] {
				rec(i+1, append(acc, m))
			}
		}
	}
	rec(0, nil)
	return out
}

// neighbourValues returns the candidate values that count as a one-step move on
// this axis from the current value.
func neighbourValues(ax Axis, cur string, has bool, idx int) []string {
	switch ax.Kind {
	case Ordinal, Numeric:
		if !has || idx < 0 {
			return ax.Values // unknown current position: every value is reachable
		}
		var n []string
		if idx-1 >= 0 {
			n = append(n, ax.Values[idx-1])
		}
		if idx+1 < len(ax.Values) {
			n = append(n, ax.Values[idx+1])
		}
		return n
	default: // Categorical
		n := make([]string, 0, len(ax.Values))
		for _, v := range ax.Values {
			if v != cur {
				n = append(n, v)
			}
		}
		return n
	}
}

func indexOf(vals []string, v string) int {
	for i, x := range vals {
		if x == v {
			return i
		}
	}
	return -1
}

func clone(s Strategy) Strategy {
	c := make(Strategy, len(s)+1)
	for k, v := range s {
		c[k] = v
	}
	return c
}
