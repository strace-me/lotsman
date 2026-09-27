package strategy

import (
	"encoding/json"
	"fmt"
	"github.com/strace-me/lotsman/pkg/atomicfile"
	"os"
)

// Discovered-strategy persistence (LOT-10a). `lotsmanctl harvest` runs blockcheck
// and writes the strategies it finds (raw NFQWSArgs + metadata) to a JSON file;
// the daemon reads that file at startup and folds the definitions into its catalog
// so the KB can rank over them and the per-service composer can render launchers
// for them. The file is a flat JSON array of Definition — ONLY discovered
// strategies live here; builtin (alt12/alt11) and config-declared strategies come
// from code/config, not this file.

// LoadDefinitions reads a JSON array of Definitions from path. A missing file is
// not an error (returns nil) — discovery simply has not run yet. A malformed file
// IS an error so a corrupt catalog is surfaced rather than silently ignored.
func LoadDefinitions(path string) ([]Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var defs []Definition
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("strategy: parse %s: %w", path, err)
	}
	return defs, nil
}

// SaveDefinitions atomically writes defs as a pretty JSON array to path (temp file
// + rename), so a crashed write never leaves the daemon reading a half-written
// catalog.
func SaveDefinitions(path string, defs []Definition) error {
	data, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// MergeDefinitions merges incoming over existing by ID (incoming wins for a
// duplicate ID), preserving existing order and appending genuinely new IDs in
// their incoming order. Lets successive harvests accumulate the search space
// instead of overwriting it, while a re-discovered strategy refreshes its
// metadata.
func MergeDefinitions(existing, incoming []Definition) []Definition {
	idx := make(map[string]int, len(existing))
	out := make([]Definition, 0, len(existing)+len(incoming))
	for _, d := range existing {
		idx[d.ID] = len(out)
		out = append(out, d)
	}
	for _, d := range incoming {
		if i, ok := idx[d.ID]; ok {
			out[i] = d
			continue
		}
		idx[d.ID] = len(out)
		out = append(out, d)
	}
	return out
}
