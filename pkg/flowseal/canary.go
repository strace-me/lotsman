package flowseal

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategyimport"
)

// EngineCanary answers the only question that matters before a bundle swap:
// would the strategy we actually run still start against the new files?
//
// It answers it by running that strategy — argv lifted from the production
// script, with the bundle root repointed at the candidate directory and the
// queue number moved to one nothing diverts traffic to. The engine validates its
// own inputs at startup (it re-reads hostlists after dropping privileges and
// exits if it cannot), which is precisely the failure that has taken the desync
// rung down three times.
//
// It is an A/B, not a single shot. The candidate result alone cannot distinguish
// "this bundle is broken" from "the engine cannot start here at all" — no
// binary, no privileges, a queue already taken — and a canary that cannot tell
// those apart would silently freeze updates forever the first time the
// environment changed. So the same argv runs against the CURRENT bundle first.
// No baseline, no verdict: the canary stands aside and says so.
type EngineCanary struct {
	// Script is the strategy the engine runs in production (e.g. the active.sh
	// symlink). Its arguments, not a synthetic set, are what gets tested.
	Script string
	// CurrentDir is the bundle root the script's paths currently resolve to.
	// Both it and the stable symlink path are rewritten to point at the candidate.
	CurrentDir string
	// LinkPath is the stable symlink the script references (e.g.
	// /opt/flowseal-current), which is what its variables actually expand to.
	LinkPath string
	// QNum is the NFQUEUE the canary binds. It MUST differ from the production
	// queue: the argv is production's, queue number and all, and reusing it would
	// have the canary fight the live engine for the queue that carries real
	// traffic. Nothing diverts to this one, so the canary sees no packets — it
	// only proves the engine accepts its inputs, which is what fails.
	QNum int
	// Run launches the engine with argv and reports nil if it was still alive a
	// moment later. Injected: this package launches nothing itself.
	Run func(ctx context.Context, prog string, argv []string) error
	Log *slog.Logger
}

// Verify implements the FileInstaller.Verify contract.
func (c *EngineCanary) Verify(newDir string) error {
	prog, argv, err := c.commandFrom(c.Script)
	if err != nil || prog == "" || len(argv) == 0 {
		c.log().Warn("flowseal canary: cannot read the production strategy, accepting the bundle unverified",
			"script", c.Script, "prog", prog, "err", err)
		return nil
	}
	if err := c.Run(context.Background(), prog, rewrite(argv, c.LinkPath, c.CurrentDir, c.CurrentDir, c.QNum)); err != nil {
		// The engine will not start on the bundle that is ALREADY in service, so
		// the canary is measuring the environment, not the release. Standing
		// aside is the safe answer: refusing every update because our own probe
		// is broken would be a worse outage than any bad bundle.
		c.log().Warn("flowseal canary: no baseline — the engine does not start on the CURRENT bundle either; accepting unverified",
			"err", err)
		return nil
	}
	if err := c.Run(context.Background(), prog, rewrite(argv, c.LinkPath, c.CurrentDir, newDir, c.QNum)); err != nil {
		return fmt.Errorf("the engine starts on the current bundle but not on this one: %w", err)
	}
	c.log().Info("flowseal canary: the engine starts on the new bundle", "dir", newDir)
	return nil
}

// commandFrom lifts the engine and its arguments out of the production strategy
// script, blocks rejoined with --new exactly as the engine takes them.
// Deliberately NOT strategyimport.Normalize: normalization is for describing a
// recipe, and what this needs is the literal command, deployment paths and all.
func (c *EngineCanary) commandFrom(path string) (string, []string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	src := strategyimport.Source{Name: path, Kind: strategyimport.KindShell, Body: body}
	blocks := strategyimport.Blocks(src)
	var argv []string
	for i, b := range blocks {
		if i > 0 {
			argv = append(argv, "--new")
		}
		argv = append(argv, b...)
	}
	return strategyimport.Program(src), argv, nil
}

// rewrite repoints every bundle path at dst, covering both the stable symlink
// the script names and the versioned directory it resolves to, and moves the
// engine onto the canary queue. It also drops the flags that would detach the
// process: a daemonized engine forks away, and a liveness check on the parent
// would then report success no matter what the child did.
func rewrite(argv []string, link, current, dst string, qnum int) []string {
	out := make([]string, 0, len(argv)+1)
	for _, a := range argv {
		// Production reaches the engine through a shell, which removes the
		// quoting; we exec it directly, so we have to. Left in, a quote becomes
		// part of the filename and every path in the command is wrong — which
		// fails the BASELINE too, so the canary would stand aside every single
		// time and never once do its job.
		a = strings.Trim(a, `"'`)
		if k, v, ok := strings.Cut(a, "="); ok {
			a = k + "=" + strings.Trim(v, `"'`)
		}
		if strings.HasPrefix(a, "--qnum=") || a == "--daemon" || strings.HasPrefix(a, "--pidfile=") {
			continue
		}
		if current != "" && current != dst {
			a = strings.ReplaceAll(a, current, dst)
		}
		if link != "" {
			a = strings.ReplaceAll(a, link, dst)
		}
		out = append(out, a)
	}
	return append([]string{fmt.Sprintf("--qnum=%d", qnum)}, out...)
}

func (c *EngineCanary) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
