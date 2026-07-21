// Package externalbox implements core.ProxyCore for the desktop client
// (Linux/Windows) by running an external sing-box binary as a managed child
// process — the same relationship the router daemon has with sing-box, but with
// a process we own instead of an OpenWrt init.d service. The in-process libbox
// core for Android lives in a separate package.
package externalbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	"github.com/strace-me/lotsman/client/core"
)

// Box runs an external `sing-box` as a managed child and implements
// core.ProxyCore.
type Box struct {
	bin        string
	configPath string
	log        *slog.Logger

	mu  sync.Mutex
	cmd *exec.Cmd
}

var _ core.ProxyCore = (*Box)(nil)

// New returns a Box that writes the live config to configPath and runs bin
// ("" defaults to "sing-box").
func New(bin, configPath string, log *slog.Logger) *Box {
	if bin == "" {
		bin = "sing-box"
	}
	return &Box{bin: bin, configPath: configPath, log: log}
}

// Check validates cfg with `sing-box check -c` against a temp file.
func (b *Box) Check(ctx context.Context, cfg []byte) error {
	tmp := b.configPath + ".check"
	if err := os.WriteFile(tmp, cfg, 0o600); err != nil {
		return fmt.Errorf("externalbox: write check config: %w", err)
	}
	defer os.Remove(tmp)
	if out, err := exec.CommandContext(ctx, b.bin, "check", "-c", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("externalbox: sing-box check: %w: %s", err, out)
	}
	return nil
}

// Start writes cfg and spawns `sing-box run`. Idempotent: a no-op if already up.
func (b *Box) Start(_ context.Context, cfg []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd != nil {
		return nil
	}
	if err := os.WriteFile(b.configPath, cfg, 0o600); err != nil {
		return fmt.Errorf("externalbox: write config: %w", err)
	}
	return b.spawnLocked()
}

// Reload writes a new config and restarts the process. sing-box has no
// connection-preserving reload, so this restarts it; the brain reasserts
// selectors afterward.
func (b *Box) Reload(_ context.Context, cfg []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.WriteFile(b.configPath, cfg, 0o600); err != nil {
		return fmt.Errorf("externalbox: write config: %w", err)
	}
	b.stopLocked()
	return b.spawnLocked()
}

// Restart re-spawns sing-box against the config already on disk, leaving the
// file untouched — the caller placed it there deliberately.
func (b *Box) Restart(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
	return b.spawnLocked()
}

// Stop kills the managed sing-box.
func (b *Box) Stop(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
	return nil
}

// Alive reports whether the managed sing-box is currently running.
func (b *Box) Alive(_ context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cmd != nil
}

// spawnLocked launches `sing-box run -c <configPath>` and reaps it in the
// background, clearing cmd when it exits. Caller holds b.mu.
func (b *Box) spawnLocked() error {
	cmd := exec.Command(b.bin, "run", "-c", b.configPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("externalbox: sing-box run: %w", err)
	}
	b.cmd = cmd
	go func() {
		err := cmd.Wait()
		b.mu.Lock()
		if b.cmd == cmd {
			b.cmd = nil
		}
		b.mu.Unlock()
		if err != nil {
			b.log.Warn("sing-box exited", "err", err)
		}
	}()
	return nil
}

// stopLocked kills the child if running. Caller holds b.mu.
func (b *Box) stopLocked() {
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		b.cmd = nil
	}
}
