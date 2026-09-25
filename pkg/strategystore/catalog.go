package strategystore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func LoadDir(root string) ([]Bundle, error) {
	engineEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var bundles []Bundle
	for _, engineEntry := range engineEntries {
		if !engineEntry.IsDir() {
			continue
		}
		engineRoot := filepath.Join(root, engineEntry.Name())
		idEntries, err := os.ReadDir(engineRoot)
		if err != nil {
			return nil, err
		}
		for _, idEntry := range idEntries {
			if !idEntry.IsDir() {
				continue
			}
			path := filepath.Join(engineRoot, idEntry.Name())
			bundle, err := LoadBundle(path)
			if err != nil {
				return nil, fmt.Errorf("strategy %s/%s: %w", engineEntry.Name(), idEntry.Name(), err)
			}
			if bundle.Manifest.Engine != engineEntry.Name() {
				return nil, fmt.Errorf("strategy %s/%s declares engine %q", engineEntry.Name(), idEntry.Name(), bundle.Manifest.Engine)
			}
			bundles = append(bundles, bundle)
		}
	}
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].Manifest.Engine != bundles[j].Manifest.Engine {
			return bundles[i].Manifest.Engine < bundles[j].Manifest.Engine
		}
		return bundles[i].Manifest.ID < bundles[j].Manifest.ID
	})
	return bundles, nil
}
