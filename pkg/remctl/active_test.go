package remctl

import (
	"fmt"
	"testing"

	"github.com/strace-me/lotsman/pkg/singbox"
)

func TestActiveSetApplyCommitSuccess(t *testing.T) {
	var a *ActiveSet
	var committed map[string]singbox.Remediation
	a = NewActiveSet(func() error {
		committed = a.Snapshot() // commit reads the desired map (no deadlock)
		return nil
	})
	if err := a.Apply("youtube", 2, singbox.Remediation{RejectQUIC: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Map advertises the remediation; commit saw it.
	if got := a.Snapshot(); len(got) != 1 || !got["youtube"].RejectQUIC {
		t.Errorf("snapshot after apply = %v, want youtube reject-quic", got)
	}
	if len(committed) != 1 || !committed["youtube"].RejectQUIC {
		t.Errorf("commit saw %v, want the applied remediation", committed)
	}
}

func TestActiveSetApplyRevertsOnCommitError(t *testing.T) {
	a := NewActiveSet(func() error { return fmt.Errorf("reconcile boom") })
	if err := a.Apply("youtube", 2, singbox.Remediation{RejectQUIC: true}); err == nil {
		t.Fatal("apply should return the commit error")
	}
	// The map must NOT advertise a remediation the live config never got (LOT-24).
	if got := a.Snapshot(); got != nil {
		t.Errorf("snapshot after failed apply = %v, want nil (reverted)", got)
	}
}

func TestActiveSetRollbackRestoresOnCommitError(t *testing.T) {
	fail := false
	a := NewActiveSet(func() error {
		if fail {
			return fmt.Errorf("reconcile boom")
		}
		return nil
	})
	if err := a.Apply("youtube", 2, singbox.Remediation{RejectQUIC: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Rollback whose commit fails must keep the entry — the live config may still
	// carry it, so the map must reflect reality (not a phantom "rolled back").
	fail = true
	if err := a.Rollback("youtube"); err == nil {
		t.Fatal("rollback should return the commit error")
	}
	if got := a.Snapshot(); len(got) != 1 || !got["youtube"].RejectQUIC {
		t.Errorf("snapshot after failed rollback = %v, want youtube still active", got)
	}
}

func TestActiveSetApplyRevertsToPriorValue(t *testing.T) {
	fail := false
	a := NewActiveSet(func() error {
		if fail {
			return fmt.Errorf("reconcile boom")
		}
		return nil
	})
	// First apply succeeds (rung 1, ip-fallback).
	if err := a.Apply("youtube", 1, singbox.Remediation{FallbackCIDRs: []string{"1.2.3.0/24"}}); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	// Second apply (escalate to reject-quic) fails to commit → must revert to the
	// prior ip-fallback value, not delete the entry.
	fail = true
	if err := a.Apply("youtube", 2, singbox.Remediation{RejectQUIC: true}); err == nil {
		t.Fatal("apply2 should error")
	}
	got := a.Snapshot()
	if len(got) != 1 || got["youtube"].RejectQUIC || len(got["youtube"].FallbackCIDRs) != 1 {
		t.Errorf("snapshot = %v, want the prior ip-fallback restored", got)
	}
}
