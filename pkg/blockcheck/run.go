package blockcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// CommandRunner runs blockcheck.sh and returns its combined output. Abstracted
// so the Runner is testable without a router (a fake returns captured output).
type CommandRunner interface {
	Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// ExecRunner runs blockcheck for real via os/exec.
type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// env holds overrides; layer them over the inherited environment so
	// blockcheck still finds PATH (curl, nft, sh) and the rest of the system.
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// Options configure one blockcheck run.
//
// Isolation note: blockcheck wants all DPI-bypass disabled — the production
// nfqws (qnum 200) would otherwise grab the test traffic and contaminate the
// result. A real discovery run must first stop the production nfqws (or exclude
// the target IPs from its nft rule) and restore it after. Simulate=true sidesteps
// this entirely (no network, no nfqws) but yields random results — use it only to
// exercise the pipeline, not to actually discover strategies.
type Options struct {
	ScriptPath string   // path to blockcheck.sh
	ZapretBase string   // zapret install root; binaries expected at <base>/binaries/<platform>
	Platform   string   // binary subdir, e.g. "linux-arm64"
	Domains    []string // targets to test
	ScanLevel  string   // "quick" | "standard" | "force" (default "standard")
	IPV        int      // 4 or 6 (default 4)
	Simulate   bool     // SIMULATE=1: dry, no data-plane touch
}

// Runner executes blockcheck and parses the winners.
type Runner struct {
	cmd CommandRunner
}

// New builds a Runner. A nil CommandRunner defaults to running for real.
func New(cmd CommandRunner) *Runner {
	if cmd == nil {
		cmd = ExecRunner{}
	}
	return &Runner{cmd: cmd}
}

// Run invokes blockcheck.sh non-interactively (BATCH=1) and returns the strategies
// it found in the SUMMARY block. The caller filters by daemon (see ZapretResults).
func (r *Runner) Run(ctx context.Context, opts Options) ([]Result, error) {
	out, err := r.RunRaw(ctx, opts)
	// blockcheck can exit non-zero yet still print a usable SUMMARY; parse
	// whatever we got so the caller can decide.
	return ParseSummary(string(out)), err
}

// RunRaw invokes blockcheck.sh and returns its raw combined output, for callers
// that need more than the SUMMARY — e.g. harvesting the full search space with
// ParseEnumerated (run with ScanLevel "force" + Simulate to dump every strategy
// blockcheck would try, without touching the data plane).
func (r *Runner) RunRaw(ctx context.Context, opts Options) ([]byte, error) {
	if opts.ScriptPath == "" {
		return nil, fmt.Errorf("blockcheck: empty script path")
	}
	if len(opts.Domains) == 0 {
		return nil, fmt.Errorf("blockcheck: no domains to test")
	}
	out, err := r.cmd.Output(ctx, buildEnv(opts), "sh", opts.ScriptPath)
	if err != nil {
		return out, fmt.Errorf("blockcheck: %w", err)
	}
	return out, nil
}

// buildEnv assembles the non-interactive environment for blockcheck.sh.
func buildEnv(opts Options) []string {
	scan := opts.ScanLevel
	if scan == "" {
		scan = "standard"
	}
	ipv := opts.IPV
	if ipv == 0 {
		ipv = 4
	}
	env := []string{
		"BATCH=1",
		"PARALLEL=0",
		"SCANLEVEL=" + scan,
		"IPV=" + strconv.Itoa(ipv),
		"DOMAINS=" + strings.Join(opts.Domains, " "),
	}
	if opts.Simulate {
		env = append(env, "SIMULATE=1")
	}
	// Point blockcheck at the real binary locations (its defaults look under
	// <base>/nfq, <base>/tpws, <base>/mdig, which the OpenWrt build does not use).
	if opts.ZapretBase != "" && opts.Platform != "" {
		bin := opts.ZapretBase + "/binaries/" + opts.Platform
		env = append(env,
			"NFQWS="+bin+"/nfqws",
			"TPWS="+bin+"/tpws",
			"MDIG="+bin+"/mdig",
		)
	}
	return env
}
