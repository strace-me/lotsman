package core

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// Several desync recipes fire a crafted packet read from a payload file, named in
// the args as e.g. --dpi-desync-split-seqovl-pattern=tls_clienthello_4pda_to.bin.
// Which payloads exist is a property of the INSTALLED zapret build, not of the
// recipe catalog: the nixpkgs package ships upstream's set and none of the
// Flowseal-specific ones, so a catalog recipe can perfectly well name a file this
// host has never had.
//
// Picking such a recipe makes nfqws fail to start, and the desync then looks
// simply ineffective — the most misleading failure available, because the honest
// verdict "this strategy is unavailable here" is indistinguishable from "this
// strategy does not work against the DPI".

// payloadRefs returns the payload filenames a recipe's args reference.
func payloadRefs(r strategycat.Recipe) []string {
	var out []string
	for _, a := range r.AllArgs() {
		_, val, ok := strings.Cut(a, "=")
		if ok && strings.HasSuffix(val, ".bin") {
			out = append(out, val)
		}
	}
	return out
}

// usablePayloadRecipes drops the recipes whose payload files are absent from dir,
// so the picker never chooses a strategy this host cannot actually run. An empty
// dir disables the check (nothing to verify against) rather than dropping
// everything.
func usablePayloadRecipes(recipes []strategycat.Recipe, dir string, log *slog.Logger) []strategycat.Recipe {
	if dir == "" {
		return recipes
	}
	out := make([]strategycat.Recipe, 0, len(recipes))
	var dropped, missingFiles []string
	for _, r := range recipes {
		// A multi-block recipe whose later profile an earlier one already claims does
		// not do what its author wrote: nfqws stops at the first match, so the block
		// behind it never runs and the knowledge base scores the bundle for a fraction
		// of itself. Refuse it whole and name the block, rather than ship a bundle
		// under its own name doing a third of the work.
		if i, why, bad := zapret.ShadowedBlock(r.AllBlocks()); bad {
			log.Warn("desync recipe refused: one of its profiles can never match",
				"recipe", r.ID, "block", i+1, "why", why)
			continue
		}
		missing := false
		for _, f := range payloadRefs(r) {
			base := filepath.Base(f)
			if _, err := os.Stat(filepath.Join(dir, base)); err != nil {
				missing = true
				if !slices.Contains(missingFiles, base) {
					missingFiles = append(missingFiles, base)
				}
				break
			}
		}
		if missing {
			dropped = append(dropped, r.ID)
			continue
		}
		out = append(out, r)
	}
	// Warn, and name the FILES, not just the recipes. A dropped recipe is
	// indistinguishable from one that never worked: the search is quietly
	// narrower, and the symptom is "the desync stopped finding anything" hours
	// later, with nothing in between pointing at a missing file. The old Info
	// line with three examples buried that.
	if len(dropped) > 0 {
		log.Warn("desync recipes UNAVAILABLE on this host: their fake-payload files are missing, so these strategies cannot be tried at all",
			"recipes_dropped", len(dropped), "of", len(recipes), "dir", dir,
			"missing_files", firstN(missingFiles, 5), "examples", firstN(dropped, 3),
			"fix", "install the payload files into dir, or point -payload-dir at a directory that has them")
	}
	return out
}

// absolutizePayloads rewrites bare payload filenames in composed args to absolute
// paths under dir. nfqws resolves a relative name against its working directory,
// which is ours, not the zapret installation's.
func absolutizePayloads(args []string, dir string) []string {
	if dir == "" {
		return args
	}
	out := make([]string, len(args))
	for i, a := range args {
		key, val, ok := strings.Cut(a, "=")
		if ok && strings.HasSuffix(val, ".bin") && !filepath.IsAbs(val) {
			out[i] = key + "=" + filepath.Join(dir, filepath.Base(val))
			continue
		}
		out[i] = a
	}
	return out
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
