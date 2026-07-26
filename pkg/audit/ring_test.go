package audit

import "testing"

func TestRingRecorderNewestFirstWithLimitAndFilter(t *testing.T) {
	r := NewRingRecorder(4)
	for _, svc := range []string{"a", "b", "a", "c"} {
		r.Record(Transition{Service: svc})
	}
	all := r.Snapshot(0, "")
	if len(all) != 4 || all[0].Service != "c" || all[3].Service != "a" {
		t.Fatalf("newest-first order wrong: %+v", all)
	}
	if got := r.Snapshot(2, ""); len(got) != 2 || got[0].Service != "c" || got[1].Service != "a" {
		t.Errorf("limit=2 wrong: %+v", got)
	}
	if got := r.Snapshot(0, "a"); len(got) != 2 {
		t.Errorf("service filter: want 2, got %d", len(got))
	}
}

func TestRingRecorderDropsOldestBeyondCapacity(t *testing.T) {
	r := NewRingRecorder(2)
	r.Record(Transition{Service: "old"})
	r.Record(Transition{Service: "mid"})
	r.Record(Transition{Service: "new"}) // evicts "old"
	got := r.Snapshot(0, "")
	if len(got) != 2 || got[0].Service != "new" || got[1].Service != "mid" {
		t.Errorf("eviction/order wrong: %+v", got)
	}
}

func TestRingSnapshotIsNonNilWhenEmpty(t *testing.T) {
	if got := NewRingRecorder(0).Snapshot(0, ""); got == nil {
		t.Error("Snapshot must return a non-nil slice so it serialises as [] not null")
	}
}

func TestRingRecordStampsZeroTime(t *testing.T) {
	r := NewRingRecorder(1)
	r.Record(Transition{Service: "a"})
	if r.Snapshot(0, "")[0].Time.IsZero() {
		t.Error("Record must stamp a zero Time")
	}
}
