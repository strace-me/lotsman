package externalbox

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReclaimIgnoresAMissingPidFile(t *testing.T) {
	// A first-ever run has no record, which is not a failure.
	got, err := ReclaimStale(filepath.Join(t.TempDir(), "absent.pid"), "/etc/lotsman/singbox.json")
	if err != nil || got != 0 {
		t.Errorf("ReclaimStale = %d, %v; want 0, nil", got, err)
	}
}

func TestReclaimDropsAGarbagePidFile(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "box.pid")
	if err := os.WriteFile(pf, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReclaimStale(pf, "/x"); err != nil || got != 0 {
		t.Errorf("ReclaimStale = %d, %v; want 0, nil", got, err)
	}
	if _, err := os.Stat(pf); !os.IsNotExist(err) {
		t.Error("an unparseable record must be discarded, not kept to fail again next time")
	}
}

func TestReclaimRefusesAPidThatIsNotOurs(t *testing.T) {
	// pids are recycled. Killing whatever now holds the number would be far worse
	// than leaving a stale process alone, so the record is only trusted when the
	// process still looks like the sing-box we started.
	dir := t.TempDir()
	pf := filepath.Join(dir, "box.pid")
	// This test process is certainly alive and certainly not our sing-box.
	if err := os.WriteFile(pf, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReclaimStale(pf, "/etc/lotsman/singbox.json")
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if got != 0 {
		t.Fatalf("reclaimed pid %d — it must never signal a process it cannot identify as ours", got)
	}
	if _, err := os.Stat(pf); !os.IsNotExist(err) {
		t.Error("a record that no longer matches must be dropped")
	}
}

func TestRecordPidRoundTrips(t *testing.T) {
	pf := filepath.Join(t.TempDir(), "nested", "box.pid")
	if err := recordPid(pf, 4242); err != nil {
		t.Fatalf("recordPid: %v", err)
	}
	raw, err := os.ReadFile(pf)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != "4242" {
		t.Errorf("recorded %q, want 4242", raw)
	}
}
