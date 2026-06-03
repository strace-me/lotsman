// Package audit persists Brain's state transitions as JSON lines, so the
// history of "what switched, when, and why" survives restarts and can be
// replayed when debugging (e.g. a service that flapped). It is deliberately
// simple: append-only JSONL, one record per transition.
package audit

import (
	"encoding/json"
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
	mu sync.Mutex
	f  *os.File
}

// NewFileRecorder opens (creating/appending) the audit log at path.
func NewFileRecorder(path string) (*FileRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileRecorder{f: f}, nil
}

func (r *FileRecorder) Record(t Transition) {
	if t.Time.IsZero() {
		t.Time = time.Now()
	}
	line, err := json.Marshal(t)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.f.Write(append(line, '\n'))
}

// Close closes the underlying file.
func (r *FileRecorder) Close() error { return r.f.Close() }
