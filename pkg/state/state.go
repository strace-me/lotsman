// Package state persists Brain's per-service chain positions so a restart
// resumes where it left off instead of resetting everyone to PREFERRED and
// re-escalating from scratch (which would briefly break services that were on
// a fallback). It is a small JSON file, written atomically.
package state

import (
	"encoding/json"
	"os"
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
