// Package throughput measures how much each exit actually CARRIES, out of band.
//
// This exists because the passive per-exit observation in pkg/observe cannot seed
// a pool. That measurement is inbound: it counts bytes crossing each exit, so it
// is blind to exits nobody is using. For "which of the exits in play is better"
// that blindness is harmless — the question only concerns exits already carrying.
// For "who should be in play" it is fatal. LOT-69's own state makes this concrete:
// ten rules on one exit means exactly one candidate is ever measured, so ranking
// the pool on the measured subset keeps that one exit and collapses the pool to a
// single member. That would look like a successful narrowing while locking in the
// monoculture the pool was meant to break up.
//
// So the measurement has to be made deliberately, per node, and that costs uplink
// bytes. pkg/pools therefore gates the narrowing on coverage (MinCoverage) and
// this scanner is what raises coverage, a few nodes per pass, so the cost is
// bounded and observable rather than a 108-node sweep on the uplink it measures.
package throughput

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/atomicfile"
)

// Reading is one node's measured throughput and when it was taken.
type Reading struct {
	KBps   float64   `json:"kbps"`
	At     time.Time `json:"at"`
	Failed bool      `json:"failed,omitempty"`
}

// A failed reading is kept, not dropped. A node that could not carry is a
// measurement, and forgetting it would make the next pass treat it as unmeasured
// — and unmeasured nodes are the ones the coverage gate refuses to rank on. The
// failure is what makes a pool honest.
const defaultStaleAfter = 30 * time.Minute

// Store holds per-node throughput readings across passes and across restarts.
//
// Persistence is not optional: coverage has to survive a restart, or every
// restart returns the gate to "too few measured" and the pool silently widens
// each time the box reboots.
type Store struct {
	mu      sync.Mutex
	path    string
	stale   time.Duration
	read    map[string]Reading
	now     func() time.Time
	lastErr error
}

// OpenStore loads readings from path, or starts empty when there is nothing to
// load. A corrupt file is not fatal: it is renamed out of the way and the scan
// starts over, because refusing to start over a scratch measurement would be a
// worse outcome than re-measuring.
func OpenStore(path string) *Store {
	s := &Store{path: path, stale: defaultStaleAfter, read: map[string]Reading{}, now: time.Now}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var loaded map[string]Reading
	if err := json.Unmarshal(data, &loaded); err != nil || loaded == nil {
		return s
	}
	s.read = loaded
	return s
}

// Record stores one reading and persists the map.
func (s *Store) Record(node string, kbps float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read[node] = Reading{KBps: kbps, At: s.now()}
	s.lastErr = s.persistLocked()
}

// RecordFailure stores "measured, carried nothing" — which is a frozen exit, not
// an idle one, and is recorded as a zero rather than as an absence.
func (s *Store) RecordFailure(node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read[node] = Reading{KBps: 0, At: s.now(), Failed: true}
	s.lastErr = s.persistLocked()
}

// Fresh returns the readings still within the staleness bound. A reading from
// yesterday does not describe today's exit, and letting it win a ranking would be
// a claim about the past presented as a fact about now.
func (s *Store) Fresh() map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make(map[string]float64, len(s.read))
	for node, r := range s.read {
		if now.Sub(r.At) > s.stale {
			continue
		}
		out[node] = r.KBps
	}
	return out
}

// Coverage reports how many of the given nodes have a fresh reading, and returns
// that fraction. This is the number the pool's gate compares against.
func (s *Store) Coverage(nodes []string) (measured int, fraction float64) {
	fresh := s.Fresh()
	for _, n := range nodes {
		if _, ok := fresh[n]; ok {
			measured++
		}
	}
	if len(nodes) > 0 {
		fraction = float64(measured) / float64(len(nodes))
	}
	return measured, fraction
}

// Unmeasured lists the nodes with no fresh reading, in stable order. The scanner
// walks this list, so coverage grows by exactly the budget each pass.
func (s *Store) Unmeasured(nodes []string) []string {
	fresh := s.Fresh()
	var out []string
	for _, n := range nodes {
		if _, ok := fresh[n]; !ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Stalest returns the nodes with a reading, oldest first, so a fully-covered pool
// still refreshes what it knows and never freezes on the first day it was measured.
func (s *Store) Stalest(nodes []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	type pair struct {
		node string
		at   time.Time
	}
	var seen []pair
	for _, n := range nodes {
		if r, ok := s.read[n]; ok {
			seen = append(seen, pair{node: n, at: r.At})
		}
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i].at.Before(seen[j].at) })
	out := make([]string, 0, len(seen))
	for _, p := range seen {
		out = append(out, p.node)
	}
	return out
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.Marshal(s.read)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, data, 0o600)
}

// Scanner spends a bounded budget per pass, measuring nodes that nobody has
// measured yet and, once the fleet is covered, refreshing the stalest readings.
//
// The order matters more than it looks. Measuring unmeasured nodes first is what
// makes coverage grow; measuring "the next K in a rotation" instead would keep
// re-measuring nodes that already have a number and never reach the ones the gate
// is waiting on.
type Scanner struct {
	Store *Store
	// Measure returns KiB/s for one node, or ok=false when the node could not be
	// reached. Injected so the scan can be tested without a network and without
	// sing-box.
	Measure func(ctx context.Context, node string) (kbps float64, ok bool)
	Log     Logger
}

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// PassReport says what one budgeted pass did, so the cost is visible rather than
// inferred from a bill.
type PassReport struct {
	Attempted int
	Measured  int
	Failed    int
	// CoverageAfter is the fleet-wide fraction that matters to the pool gate.
	CoverageAfter float64
	MeasuredTotal int
}

// Pass measures at most budget nodes.
func (s *Scanner) Pass(ctx context.Context, nodes []string, budget int) PassReport {
	var report PassReport
	if s.Store == nil || s.Measure == nil || budget <= 0 {
		return report
	}
	queue := s.Store.Unmeasured(nodes)
	// Once every candidate has a reading, the interesting work becomes keeping
	// them true, so the stalest go first.
	if len(queue) == 0 {
		queue = s.Store.Stalest(nodes)
	}
	for _, node := range queue {
		if report.Attempted >= budget {
			break
		}
		if err := ctx.Err(); err != nil {
			break
		}
		report.Attempted++
		kbps, ok := s.Measure(ctx, node)
		if !ok {
			s.Store.RecordFailure(node)
			report.Failed++
			s.Log.Warn("throughput: node could not be measured", "node", node)
			continue
		}
		s.Store.Record(node, kbps)
		report.Measured++
	}
	report.MeasuredTotal, report.CoverageAfter = s.Store.Coverage(nodes)
	return report
}
