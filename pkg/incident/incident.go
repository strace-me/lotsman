// Package incident persists self-heal remediation events as JSON lines, so the
// life of an auto-remediation — detected, applied, then resolved, rolled-back,
// or escalated — survives restarts and can be reviewed after the fact ("what
// did the armed controller do to my router last night?").
//
// It is the audit trail for the LOT-18b armed remediation ladder. Where
// pkg/audit records Brain's selector/strategy transitions and pkg/faillog
// records raw probe failures, this records the remediation controller's
// decisions on the live sing-box config. One record per phase transition.
package incident

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Incident phases — the lifecycle of one remediation attempt for a service.
const (
	PhaseDetected   = "detected"    // misroute confirmed past hysteresis; about to act
	PhaseApplied    = "applied"     // remediation injected + reconcile triggered
	PhaseResolved   = "resolved"    // canary window saw health recover; remediation kept
	PhaseRolledBack = "rolled-back" // canary window failed; remediation removed
	PhaseEscalated  = "escalated"   // advanced to the next ladder rung after a rollback
)

// Incident is one remediation-lifecycle event for a service.
type Incident struct {
	Time          time.Time `json:"time"`
	Service       string    `json:"service"`
	Kind          string    `json:"kind"` // misroute kind: "leak" | "dead" (empty when not from a verdict)
	LeakRatio     float64   `json:"leak_ratio"`
	DeadFlowRatio float64   `json:"dead_flow_ratio"`
	Flows         int       `json:"flows"`
	Rung          int       `json:"rung"`   // ladder rung: 1 ip-fallback, 2 reject-quic, 3 escalate-node
	Action        string    `json:"action"` // remediate action name (ip-fallback|reject-quic|escalate-node)
	CIDRs         []string  `json:"cidrs,omitempty"`
	Phase         string    `json:"phase"` // PhaseDetected | PhaseApplied | PhaseResolved | PhaseRolledBack | PhaseEscalated
	Note          string    `json:"note,omitempty"`
}

// Recorder persists incidents.
type Recorder interface {
	Record(Incident)
}

// Nop discards incidents (used when no incident log is configured).
type Nop struct{}

func (Nop) Record(Incident) {}

// FileRecorder appends incidents as JSON lines to a file.
type FileRecorder struct {
	mu  sync.Mutex
	f   *os.File
	log *slog.Logger
}

// NewFileRecorder opens (creating/appending) the incident log at path. log may
// be nil, in which case marshal/write failures are reported via slog.Default.
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

func (r *FileRecorder) Record(in Incident) {
	if in.Time.IsZero() {
		in.Time = time.Now()
	}
	line, err := json.Marshal(in)
	if err != nil {
		r.log.Warn("incident: drop record, marshal failed", "service", in.Service, "err", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(append(line, '\n')); err != nil {
		r.log.Warn("incident: write failed", "service", in.Service, "err", err)
	}
}

// Close closes the underlying file.
func (r *FileRecorder) Close() error { return r.f.Close() }
