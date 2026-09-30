package main

import (
	"fmt"
	"os"

	"github.com/strace-me/lotsman/pkg/config"
)

// loadV2Config reads a v2 document and projects it onto the runtime Config.
//
// It reuses the v2 loader the daemon's preflight uses, so a config that generates
// here is one the daemon also accepts — a second reader would mean the CLI and the
// daemon disagree about what a config IS, which is the one class of bug a preflight
// exists to catch.
//
// The gaps list is printed rather than swallowed: the projection leaves DNS, uTLS,
// the target sing-box version and declared strategies at zero, and a config that
// generates "fine" with all four silently defaulted must say so.
func loadV2Config(configPath, rulesDir, storeRoot string) (*config.Config, error) {
	// ONE loader for both roots. A second reader here would mean a config that
	// validates in the CLI and is rejected by the daemon — the one class of bug a
	// preflight exists to catch.
	load, err := config.LoadV2(configPath, rulesDir, storeRoot)
	if err != nil {
		return nil, err
	}
	if load.StoreMissing != nil {
		fmt.Fprintf(os.Stderr, "v2 note: %v (a pinned strategy cannot be verified until it exists)\n", load.StoreMissing)
	}
	projected, gaps, err := config.FromV2(load.Config, load.Rules)
	if err != nil {
		return nil, err
	}
	// The gaps are printed, never swallowed: the projection leaves DNS, uTLS and the
	// declared strategies at zero, and a config that generates "fine" with all
	// three defaulted must say so.
	for _, gap := range gaps {
		fmt.Fprintf(os.Stderr, "v2 gap: %s\n", gap)
	}
	on, off := config.EnabledRules(load.Rules)
	fmt.Fprintf(os.Stderr, "v2: %d rule(s), %d enabled, %d disabled\n", len(load.Rules), len(on), len(off))
	return projected, nil
}
