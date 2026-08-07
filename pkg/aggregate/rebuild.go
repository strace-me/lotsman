package aggregate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RebuildSpec is one declared list to keep fresh: fetch every source, subtract the
// exclude sources, and write the merged domains to Out.
//
// It mirrors config.Hostlist field for field, but lives here so both front-ends can
// share the rebuild without this package importing pkg/config — which depends on this
// one, so the arrow may not point back.
type RebuildSpec struct {
	Name    string
	Out     string
	Sources []Source
	Exclude []Source
	// Domains are the operator's OWN entries, merged in alongside whatever the
	// sources yield. A pack may consist of nothing else: "the four hosts my bank
	// uses" is a reusable list, and until this existed the only way to declare one
	// was to publish it at a URL and fetch it back.
	Domains      []string
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
// It reports changed=true only when the file on disk actually moved, which the caller
// uses to tell the operator the running config is now a refresh behind.
func Rebuild(ctx context.Context, m *Manager, spec RebuildSpec, dryRun bool, log *slog.Logger) (changed bool, err error) {
	res, errs := m.Build(ctx, spec.Sources, spec.Exclude)
	for _, e := range errs {
		log.Warn("hostlist source issue", "list", spec.Name, "err", e)
	}
	// The operator's own entries go in AFTER exclusion, deliberately. An upstream
	// exclude list quietly deleting a domain somebody typed by hand would be
	// invisible and maddening; what you wrote yourself is the most explicit
	// statement of intent in the file and outranks a third party's opinion.
	domains, own := mergeOwn(res.Domains, spec.Domains)
	if len(domains) == 0 {
		return false, fmt.Errorf("hostlist %q: merged to zero domains, keeping existing file", spec.Name)
	}
	log.Info("hostlist built", "list", spec.Name, "domains", len(domains), "own", own,
		"sources", res.Sources, "excluded", res.Excluded, "invalid", res.Invalid, "out", spec.Out)
	res.Domains = domains

	if prev := CountLines(spec.Out); !ShrinkOK(prev, len(res.Domains), spec.MinKeepRatio) {
		log.Warn("hostlist shrink guard tripped, keeping existing file", "list", spec.Name,
			"prev", prev, "new", len(res.Domains), "min_ratio", spec.MinKeepRatio)
		return false, nil
	}

	if dryRun {
		log.Info("dry-run: would write hostlist", "list", spec.Name, "out", spec.Out)
		return false, nil
	}

	body := []byte(strings.Join(res.Domains, "\n") + "\n")
	// The operator picks the path; nothing guarantees its parent exists. WriteIfChanged
	// writes a temp file NEXT TO the target first (so the swap is atomic), so a missing
	// directory fails at the temp file with "no such file or directory" — an error about
	// a path the operator never typed. Measured the day packs arrived: the lists fetched
	// and merged correctly and the write failed every cycle, on `out:` pointing one
	// directory deeper than the default.
	if dir := filepath.Dir(spec.Out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("hostlist %q: out dir %s: %w", spec.Name, dir, err)
		}
	}
	changed, err = WriteIfChanged(spec.Out, body, 0o644)
	if err != nil {
		return false, fmt.Errorf("hostlist %q: write: %w", spec.Name, err)
	}
	if changed {
		log.Info("hostlist written", "list", spec.Name, "out", spec.Out, "domains", len(res.Domains))
	} else {
		log.Info("hostlist unchanged, skipped write", "list", spec.Name, "out", spec.Out)
	}
	return changed, nil
}

// mergeOwn folds the operator's hand-written entries into a fetched set, and
// reports how many of them were new. They go through ParseList like any other
// list text, so they get the same lowercasing, comment stripping and rejection of
// junk — a domain typed into the app must not be held to a laxer standard than
// one fetched from a URL. The result is sorted, because the caller writes it only
// when the bytes changed and an unstable order would rewrite the file forever.
func mergeOwn(fetched, own []string) ([]string, int) {
	if len(own) == 0 {
		return fetched, 0
	}
	parsed, _ := ParseList([]byte(strings.Join(own, "\n")))
	seen := make(map[string]bool, len(fetched)+len(parsed))
	out := make([]string, 0, len(fetched)+len(parsed))
	for _, d := range fetched {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	added := 0
	for _, d := range parsed {
		d = strings.TrimPrefix(d, "*.")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
		added++
	}
	sort.Strings(out)
	return out, added
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
