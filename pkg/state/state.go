// Package state persists Brain's per-service chain positions so a restart
// resumes where it left off instead of resetting everyone to PREFERRED and
// re-escalating from scratch (which would briefly break services that were on
// a fallback). It is a small JSON file, written atomically.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// FileStore persists positions to a JSON file: {"service": position}.
type FileStore struct {
	path string
}

// NewFileStore returns a store backed by path.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// Load reads saved positions. A missing or unreadable file yields an empty map
// (cold start), never an error — persistence is best-effort.
func (s *FileStore) Load() map[string]int {
	out := map[string]int{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// Save writes positions atomically (temp file + rename) so a crash mid-write
// cannot corrupt the state file.
func (s *FileStore) Save(positions map[string]int) error {
	data, err := json.Marshal(positions)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// PathFor derives a per-network store path from a base one: /var/lib/lotsman/
// state.json with network "86bddb09" becomes state.86bddb09.json. An empty
// network id returns base unchanged.
//
// Positions are per-network for the same reason the knowledge base is (LOT-62).
// A chain position is an answer to "which rung works HERE", and one number shared
// across every network makes the rung a laptop ended on at the office the rung it
// starts from at home — where the honest answer is usually a different one, and
// the walk to it is paid in broken service on every arrival.
func PathFor(base, networkID string) string {
	if base == "" || networkID == "" {
		return base
	}
	ext := filepath.Ext(base)             // ".json"
	stem := strings.TrimSuffix(base, ext) // "/var/lib/lotsman/state"
	return stem + "." + networkID + ext
}
