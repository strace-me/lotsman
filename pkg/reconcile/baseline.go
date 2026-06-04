package reconcile

import (
	"encoding/json"
	"os"
)

// BaselineStore persists the reconciler's anti-churn node-count baseline
// (lastNodes) across daemon restarts, so the degraded-fetch guard works on the
// very first reconcile after a restart instead of resetting to 0 and applying a
// degraded startup fetch wholesale (LOT-29). It is a tiny JSON file written
// atomically, mirroring pkg/state's FileStore.
type BaselineStore struct {
	path string
}

// NewBaselineStore returns a store backed by path.
func NewBaselineStore(path string) *BaselineStore { return &BaselineStore{path: path} }

// baselineFile is the on-disk shape: {"last_nodes": N}.
type baselineFile struct {
	LastNodes int `json:"last_nodes"`
}

// Load returns the saved baseline node count. A missing, unreadable, or corrupt
// file yields 0 (cold start), never an error — persistence is best-effort.
func (s *BaselineStore) Load() int {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return 0
	}
	var f baselineFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0
	}
	return f.LastNodes
}

// Save writes the baseline atomically (temp file + rename) so a crash mid-write
// cannot corrupt it.
func (s *BaselineStore) Save(n int) error {
	data, err := json.Marshal(baselineFile{LastNodes: n})
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
