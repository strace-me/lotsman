package rulesets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/strace-me/lotsman/pkg/aggregate"
)

// PathFor maps a rule-set tag to its file path RELATIVE to the rule-set root
// (e.g. "geosite-youtube" -> "rule-set-geosite/geosite-youtube.srs"), and ok=false
// for a tag with no known layout (skipped). The caller supplies it so this
// package stays decoupled from the sing-box generator's directory convention.
type PathFor func(tag string) (rel string, ok bool)

// Apply executes a Plan: for every Swap tag it atomically copies the candidate's
// .srs from srcDir into liveDir at the same relative path; KeepOld and Missing
// tags are left untouched. Writes are skip-if-equal (aggregate.WriteIfChanged) so
// an unchanged file costs no disk write — most refreshes touch few tags. Returns
// the tags whose on-disk bytes actually changed (for logging). A swap whose
// source file is unreadable, or whose tag has no PathFor mapping, is an error
// (the plan referenced a tag the release was supposed to provide).
func Apply(p Plan, srcDir, liveDir string, pathFor PathFor) (changed []string, err error) {
	if p.Unusable {
		return nil, fmt.Errorf("rulesets: refusing to apply Unusable plan (missing tags %v)", p.Missing)
	}
	for _, tag := range p.Swap {
		rel, ok := pathFor(tag)
		if !ok {
			return changed, fmt.Errorf("rulesets: no path mapping for tag %q", tag)
		}
		body, err := os.ReadFile(filepath.Join(srcDir, rel))
		if err != nil {
			return changed, fmt.Errorf("rulesets: read candidate %q: %w", tag, err)
		}
		dst := filepath.Join(liveDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return changed, fmt.Errorf("rulesets: mkdir for %q: %w", tag, err)
		}
		did, err := aggregate.WriteIfChanged(dst, body, 0o644)
		if err != nil {
			return changed, fmt.Errorf("rulesets: write %q: %w", tag, err)
		}
		if did {
			changed = append(changed, tag)
		}
	}
	sort.Strings(changed)
	return changed, nil
}
