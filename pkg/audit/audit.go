// Package audit persists Brain's state transitions as JSON lines, so the
// history of "what switched, when, and why" survives restarts and can be
// replayed when debugging (e.g. a service that flapped). It is deliberately
// simple: append-only JSONL, one record per transition.
package audit

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Transition is one state change for a service.
type Transition struct {
	Time          time.Time `json:"time"`
	Service       string    `json:"service"`
	FromPosition  int       `json:"from_position"`
	ToPosition    int       `json:"to_position"`
	State         string    `json:"state"`
	StrategyClass string    `json:"strategy_class"`
	StrategyID    string    `json:"strategy_id"`
	Reason        string    `json:"reason"`
}

// Recorder persists transitions.
type Recorder interface {
	Record(Transition)
}

// Nop discards transitions (used when no audit log is configured).
type Nop struct{}

func (Nop) Record(Transition) {}

// FileRecorder appends transitions as JSON lines to a file.
type FileRecorder struct {
	mu  sync.Mutex
	f   *os.File
	log *slog.Logger
}

// NewFileRecorder opens (creating/appending) the audit log at path. log may be
// nil, in which case marshal/write failures are reported via slog.Default.
func NewFileRecorder(path string, log *slog.Logger) (*FileRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &FileRecorder{f: f, log: log}, nil
}

func (r *FileRecorder) Record(t Transition) {
	if t.Time.IsZero() {
		t.Time = time.Now()
	}
	line, err := json.Marshal(t)
	if err != nil {
		r.log.Warn("audit: drop transition, marshal failed", "service", t.Service, "err", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(append(line, '\n')); err != nil {
		r.log.Warn("audit: write failed", "service", t.Service, "err", err)
	}
}

// Close closes the underlying file.
func (r *FileRecorder) Close() error { return r.f.Close() }

// RingRecorder keeps the most recent transitions in memory for a UI to read back
// (the "история событий" surface). It is bounded — the oldest records fall off —
// because a UI wants a recent window, not an unbounded log. Safe for concurrent
// Record and Snapshot.
type RingRecorder struct {
	mu   sync.Mutex
	buf  []Transition
	next int // index of the next write
	n    int // records held (<= cap)
	cap  int
}

// NewRingRecorder holds the last capacity transitions (capacity <= 0 defaults to 256).
func NewRingRecorder(capacity int) *RingRecorder {
	if capacity <= 0 {
		capacity = 256
	}
	return &RingRecorder{buf: make([]Transition, capacity), cap: capacity}
}

func (r *RingRecorder) Record(t Transition) {
	if t.Time.IsZero() {
		t.Time = time.Now()
	}
	r.mu.Lock()
	r.buf[r.next] = t
	r.next = (r.next + 1) % r.cap
	if r.n < r.cap {
		r.n++
	}
	r.mu.Unlock()
}

// Snapshot returns up to limit transitions newest-first, optionally filtered to one
// service. limit <= 0 returns all retained. The result is always non-nil so it
// serialises as a JSON array, not null.
func (r *RingRecorder) Snapshot(limit int, service string) []Transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Transition, 0, r.n)
	for i := 0; i < r.n; i++ {
		idx := (r.next - 1 - i + 2*r.cap) % r.cap // walk newest -> oldest
		t := r.buf[idx]
		if service != "" && t.Service != service {
			continue
		}
		out = append(out, t)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
