package aggregate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// RebuildSpec is one declared list to keep fresh: fetch every source, subtract the
// exclude sources, and write the merged domains to Out.
//
// It mirrors config.Hostlist field for field, but lives here so both front-ends can
// share the rebuild without this package importing pkg/config — which depends on this
// one, so the arrow may not point back.
type RebuildSpec struct {
	Name         string
	Out          string
	Sources      []Source
	Exclude      []Source
	MinKeepRatio float64 // reject a rebuild below this fraction of the last good count (0 = disabled)
}

// Rebuild refreshes one list on disk. It is deliberately conservative: it would
// rather keep yesterday's working list than install a broken one, because these
// files decide which domains get desynced — an empty or half-fetched list silently
// stops bypassing the censor.
//
// Two guards, both keep-the-old rather than error: a merge that yields zero domains
// (every source unreachable), and a merge that collapsed below MinKeepRatio of the
// last good count (an upstream that half-broke). A source that fails individually is
// logged and skipped, since one dead mirror must not discard the others.
func Rebuild(ctx context.Context, m *Manager, spec RebuildSpec, dryRun bool, log *slog.Logger) error {
	res, errs := m.Build(ctx, spec.Sources, spec.Exclude)
	for _, err := range errs {
		log.Warn("hostlist source issue", "list", spec.Name, "err", err)
	}
	if len(res.Domains) == 0 {
		return fmt.Errorf("hostlist %q: merged to zero domains, keeping existing file", spec.Name)
	}
	log.Info("hostlist built", "list", spec.Name, "domains", len(res.Domains),
		"sources", res.Sources, "excluded", res.Excluded, "invalid", res.Invalid, "out", spec.Out)

	if prev := CountLines(spec.Out); !ShrinkOK(prev, len(res.Domains), spec.MinKeepRatio) {
		log.Warn("hostlist shrink guard tripped, keeping existing file", "list", spec.Name,
			"prev", prev, "new", len(res.Domains), "min_ratio", spec.MinKeepRatio)
		return nil
	}

	if dryRun {
		log.Info("dry-run: would write hostlist", "list", spec.Name, "out", spec.Out)
		return nil
	}

	body := []byte(strings.Join(res.Domains, "\n") + "\n")
	changed, err := WriteIfChanged(spec.Out, body, 0o644)
	if err != nil {
		return fmt.Errorf("hostlist %q: write: %w", spec.Name, err)
	}
	if changed {
		log.Info("hostlist written", "list", spec.Name, "out", spec.Out, "domains", len(res.Domains))
	} else {
		log.Info("hostlist unchanged, skipped write", "list", spec.Name, "out", spec.Out)
	}
	return nil
}

// CountLines counts non-blank lines in an existing list file (the last good domain
// count), or 0 if the file is missing — the input to the shrink guard.
func CountLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
