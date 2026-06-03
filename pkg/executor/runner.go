package executor

import (
	"context"
	"fmt"
	"os/exec"
)

// Runner runs a system command. Abstracted so executors that shell out
// (nft, init scripts, ln) are testable without touching the host.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// ExecRunner runs commands for real via os/exec.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return nil
}
