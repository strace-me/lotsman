package faillog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileRecorderAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fails.jsonl")
	r, err := NewFileRecorder(path)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}

	r.Record(Failure{Service: "youtube", Position: 1, Kind: "active", StrategyID: "alt12", RTTms: 5000, Err: "timeout"})
	r.Record(Failure{Service: "discord", Position: 0, Kind: "active", StrategyID: "direct", Err: "no STUN response: i/o timeout"})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var got []Failure
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var fl Failure
		if err := json.Unmarshal(sc.Bytes(), &fl); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		got = append(got, fl)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if got[0].Service != "youtube" || got[0].StrategyID != "alt12" || got[0].Err != "timeout" {
		t.Errorf("record 0 mismatch: %+v", got[0])
	}
	// STUN failure must be reviewable as a voice/UDP failure.
	if got[1].Service != "discord" || got[1].Err == "" {
		t.Errorf("record 1 mismatch: %+v", got[1])
	}
	// Time is stamped automatically when zero.
	if got[0].Time.IsZero() {
		t.Error("expected auto-stamped time")
	}
}

func TestRecordPreservesExplicitTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fails.jsonl")
	r, _ := NewFileRecorder(path)
	defer r.Close()

	want := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	r.Record(Failure{Time: want, Service: "x"})

	data, _ := os.ReadFile(path)
	var fl Failure
	if err := json.Unmarshal(data[:len(data)-1], &fl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !fl.Time.Equal(want) {
		t.Errorf("time: want %v, got %v", want, fl.Time)
	}
}

func TestNopDoesNotPanic(t *testing.T) {
	Nop{}.Record(Failure{Service: "x"})
}
