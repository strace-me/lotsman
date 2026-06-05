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

// Grid returns candidate strategies as the cartesian product of every non-empty
// axis's Values. With cap > 0 the result is truncated to the first cap
// combinations (a deterministic bound against the combinatorial blow-up — the
// tuner can sample or prioritise axes later); cap <= 0 means no limit. The empty
// strategy is returned when there are no varying axes.
func Grid(e Engine, cap int) []Strategy {
	out := []Strategy{{}}
	for _, ax := range e.Axes() {
		if len(ax.Values) == 0 {
			continue
		}
		next := make([]Strategy, 0, len(out)*len(ax.Values))
		for _, base := range out {
			for _, v := range ax.Values {
				s := clone(base)
				s[ax.Name] = v
				next = append(next, s)
			}
		}
		if cap > 0 && len(next) > cap {
			next = next[:cap]
		}
		out = next
	}
	return out
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
