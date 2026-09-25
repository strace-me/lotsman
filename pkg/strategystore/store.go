package strategystore

import (
	"sync"
)

type Store struct {
	root    string
	mu      sync.RWMutex
	bundles []Bundle
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Reload() error {
	loaded, err := LoadDir(s.root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.bundles = loaded
	s.mu.Unlock()
	return nil
}

func (s *Store) List() []Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Bundle, len(s.bundles))
	copy(out, s.bundles)
	return out
}
