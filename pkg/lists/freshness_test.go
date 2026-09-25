package lists

import (
	"testing"
	"time"
)

func TestSnapshotStaleUsesNewestSource(t *testing.T) {
	old := time.Now().Add(-8 * time.Hour)
	fresh := time.Now().Add(-time.Hour)
	snapshot := Snapshot{UpdatedAt: fresh}
	if snapshot.Stale(6 * time.Hour) {
		t.Error("snapshot with a one-hour-old source must not be stale")
	}
	snapshot.UpdatedAt = old
	if !snapshot.Stale(6 * time.Hour) {
		t.Error("snapshot whose newest source is eight hours old must be stale")
	}
}

func TestInlineSnapshotIsNeverStale(t *testing.T) {
	if (Snapshot{}).Stale(6 * time.Hour) {
		t.Error("a snapshot without timestamps must not be reported stale")
	}
}
