// Package faillog persists individual probe failures as JSON lines, so they can
// be reviewed after the fact ("what was failing while my Discord call dropped?").
//
// It is distinct from pkg/audit: audit records state *transitions* (what
// switched and why), while a service can fail probes repeatedly without ever
// transitioning. This is the raw failure stream — one record per failed probe.
package faillog

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Failure is one failed probe outcome with the context available at probe time.
type Failure struct {
	Time       time.Time `json:"time"`
	Service    string    `json:"service"`
	Position   int       `json:"position"`
	Kind       string    `json:"kind"` // "active" or "silent" (recovery probe)
	StrategyID string    `json:"strategy_id"`
	RTTms      int       `json:"rtt_ms"`
	Err        string    `json:"err"` // probe error; for STUN this names a voice/UDP failure
}

// Recorder persists probe failures.
type Recorder interface {
	Record(Failure)
}

// Nop discards failures (used when no fail log is configured).
type Nop struct{}

func (Nop) Record(Failure) {}

// FileRecorder appends failures as JSON lines to a file.
type FileRecorder struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileRecorder opens (creating/appending) the fail log at path.
func NewFileRecorder(path string) (*FileRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileRecorder{f: f}, nil
}

func (r *FileRecorder) Record(fl Failure) {
	if fl.Time.IsZero() {
		fl.Time = time.Now()
	}
	line, err := json.Marshal(fl)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.f.Write(append(line, '\n'))
}

// Close closes the underlying file.
func (r *FileRecorder) Close() error { return r.f.Close() }
