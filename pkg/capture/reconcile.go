package capture

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Runner runs a system command (executor.ExecRunner satisfies it). Injected so
// the apply path is testable without touching the live nft ruleset.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Reconciler owns the capture nft table (TM-2b/c): it diffs the desired table
// (from Model) against the live one and, when armed, atomically swaps it in —
// validate (`nft -c`) → backup → write+`nft -f` → rollback to the previous live
// table on failure. The nft-side analogue of pkg/reconcile (sing-box config).
// Defaults to PROPOSE-ONLY (logs what it would change, touches nothing).
type Reconciler struct {
	Model       Model
	RulesetPath string                                    // generated file path (e.g. /etc/nftables.d/10-lotsman-capture.nft)
	BackupDir   string                                    // timestamped backups of the live table before a swap
	Runner      Runner                                    // runs `nft -c -f` / `nft -f`
	LiveTable   func(ctx context.Context) ([]byte, error) // returns `nft list table ip <name>` output
	NftBin      string                                    // nft binary ("" => "nft")
	Now         func() time.Time                          // injectable clock (nil = time.Now)
	DynBypass   func() []Bypass                           // TM-5: learned bypass sets, appended to Model.BypassSets each pass (nil = none)
	Log         *slog.Logger

	armed bool
}

// effectiveModel folds the learned (dynamic) bypass sets into the base model, so
// the generated table reflects what the Eye learned this pass.
func (r *Reconciler) effectiveModel() Model {
	m := r.Model
	if r.DynBypass != nil {
		if dyn := r.DynBypass(); len(dyn) > 0 {
			m.BypassSets = append(append([]Bypass{}, m.BypassSets...), dyn...)
		}
	}
	return m
}

// Arm makes the reconciler the single writer of the capture table (validate +
// atomic apply + rollback). Without it the reconciler is propose-only.
func (r *Reconciler) Arm() { r.armed = true }

// Reconcile runs one pass: generate the desired table, diff it against the live
// one, and (propose-only) log a needed change or (armed) atomically apply it.
// No-op when desired == live (churn-guard). Shaped as periodic.Task.Fn.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	model := r.effectiveModel()
	desired := GenerateNft(model)
	live, err := r.LiveTable(ctx)
	if err != nil {
		// Can't read live (table missing on a fresh boot, or nft hiccup): treat as
		// "differs" so an armed reconciler installs it; propose-only just logs.
		r.log().Warn("capture: cannot read live table, treating as differs", "err", err)
		live = nil
	}
	if bytes.Equal(bytes.TrimSpace(desired), bytes.TrimSpace(live)) {
		return nil // parity: nothing to do (no eMMC write, no nft churn)
	}

	apply := applyScript(model)
	if err := r.validate(ctx, apply); err != nil {
		return fmt.Errorf("capture: nft validation failed (not applied): %w", err)
	}

	if !r.armed {
		r.log().Info("capture: table differs and passes nft -c (PROPOSE-ONLY, not applied)",
			"path", r.RulesetPath, "bypass_sets", len(model.BypassSets), "desired_bytes", len(desired), "live_bytes", len(live))
		return nil
	}
	return r.apply(ctx, apply, live)
}

// applyScript wraps the desired table in an atomic delete-and-recreate so a single
// `nft -f` replaces the whole table (add-ensures-exists so delete can't fail,
// delete clears the old chains, then the canonical table block recreates it).
func applyScript(m Model) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "add table ip %s\n", m.Table)
	fmt.Fprintf(&b, "delete table ip %s\n", m.Table)
	b.Write(GenerateNft(m))
	return []byte(b.String())
}

func (r *Reconciler) validate(ctx context.Context, script []byte) error {
	tmp, err := os.CreateTemp("", "lotsman-capture-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(script); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return r.Runner.Run(ctx, r.nft(), "-c", "-f", tmp.Name())
}

// apply backs up the live table, writes the desired ruleset, loads it atomically,
// and verifies the table now matches desired — rolling back to the previous live
// table if the load or verify fails.
func (r *Reconciler) apply(ctx context.Context, script, live []byte) error {
	if r.BackupDir != "" && len(live) > 0 {
		bak := filepath.Join(r.BackupDir, "capture-"+r.now().Format("20060102-150405")+".nft")
		if err := os.WriteFile(bak, live, 0o644); err != nil {
			r.log().Warn("capture: backup write failed (continuing)", "err", err)
		}
	}
	if err := os.WriteFile(r.RulesetPath, script, 0o644); err != nil {
		return fmt.Errorf("capture: write ruleset: %w", err)
	}
	if err := r.Runner.Run(ctx, r.nft(), "-f", r.RulesetPath); err != nil {
		r.rollback(ctx, live)
		return fmt.Errorf("capture: nft -f failed, rolled back: %w", err)
	}
	// Sanity (not byte-exact): the script was nft -c validated and nft -f is an
	// atomic transaction, so a clean load means the table IS what we wrote. We do
	// NOT byte-compare against `nft list` here — nft re-canonicalises output (set
	// ordering/merging), so an exact compare could false-trip a rollback even on a
	// correct load. We only catch a catastrophic load (empty/absent table).
	if now, err := r.LiveTable(ctx); err == nil && len(bytes.TrimSpace(now)) == 0 {
		r.rollback(ctx, live)
		return fmt.Errorf("capture: post-apply table empty, rolled back")
	}
	r.log().Info("capture: table reconciled (applied)", "path", r.RulesetPath)
	return nil
}

// rollback re-applies the previous live table (wrapped in the same atomic
// delete-recreate). Best-effort: failure is logged, not returned.
func (r *Reconciler) rollback(ctx context.Context, live []byte) {
	if len(live) == 0 {
		r.log().Error("capture: no previous table to roll back to")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "add table ip %s\ndelete table ip %s\n", r.Model.Table, r.Model.Table)
	b.Write(live)
	tmp, err := os.CreateTemp("", "lotsman-capture-rb-*.nft")
	if err != nil {
		r.log().Error("capture: rollback temp failed", "err", err)
		return
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(b.String())
	tmp.Close()
	if err := r.Runner.Run(ctx, r.nft(), "-f", tmp.Name()); err != nil {
		r.log().Error("capture: ROLLBACK failed — capture table may be inconsistent", "err", err)
	}
}

func (r *Reconciler) nft() string {
	if r.NftBin != "" {
		return r.NftBin
	}
	return "nft"
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
