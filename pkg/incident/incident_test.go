package incident

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordWritesJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incident.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	defer r.Close()

	ts := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	r.Record(Incident{
		Time:      ts,
		Service:   "youtube",
		Kind:      "leak",
		LeakRatio: 0.66,
		Flows:     12,
		Rung:      1,
		Action:    "ip-fallback",
		CIDRs:     []string{"142.251.0.0/16"},
		Phase:     PhaseApplied,
		Note:      "confirmed past hysteresis",
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("line not newline-terminated: %q", data)
	}
	var got Incident
	if err := json.Unmarshal(bytes.TrimSpace(data), &got); err != nil {
		t.Fatalf("Unmarshal: %v (line=%q)", err, data)
	}
	if got.Service != "youtube" || got.Rung != 1 || got.Action != "ip-fallback" ||
		got.Phase != PhaseApplied || !got.Time.Equal(ts) || len(got.CIDRs) != 1 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestRecordAppendsAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incident.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	r.Record(Incident{Service: "a", Phase: PhaseDetected})
	r.Record(Incident{Service: "b", Phase: PhaseApplied})
	r.Close()

	r2, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r2.Record(Incident{Service: "c", Phase: PhaseResolved})
	r2.Close()

	f, _ := os.Open(path)
	defer f.Close()
	var svcs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var in Incident
		if err := json.Unmarshal(sc.Bytes(), &in); err != nil {
			t.Fatalf("Unmarshal line: %v", err)
		}
		svcs = append(svcs, in.Service)
	}
	if got := strings.Join(svcs, ","); got != "a,b,c" {
		t.Errorf("append order = %q, want a,b,c", got)
	}
}

func TestRecordAutoStampsTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incident.jsonl")
	r, _ := NewFileRecorder(path, nil)
	defer r.Close()
	r.Record(Incident{Service: "x", Phase: PhaseDetected})

	data, _ := os.ReadFile(path)
	var in Incident
	if err := json.Unmarshal(bytes.TrimSpace(data), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Time.IsZero() {
		t.Error("expected auto-stamped time")
	}
}

func TestConcurrentRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incident.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Record(Incident{Service: "svc", Rung: i % 3, Phase: PhaseApplied})
		}(i)
	}
	wg.Wait()
	r.Close()

	f, _ := os.Open(path)
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var in Incident
		if err := json.Unmarshal(sc.Bytes(), &in); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		count++
	}
	if count != n {
		t.Errorf("want %d records, got %d", n, count)
	}
}

func TestNopDoesNotPanic(t *testing.T) {
	Nop{}.Record(Incident{Service: "x", Phase: PhaseApplied})
}
