package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordWritesJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	defer r.Close()

	ts := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	r.Record(Transition{
		Time:         ts,
		Service:      "youtube",
		FromPosition: 0,
		ToPosition:   1,
		State:        "escalated",
		Reason:       "fails",
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("line not newline-terminated: %q", data)
	}
	var got Transition
	if err := json.Unmarshal(bytes.TrimSpace(data), &got); err != nil {
		t.Fatalf("Unmarshal: %v (line=%q)", err, data)
	}
	if got.Service != "youtube" || got.ToPosition != 1 || got.State != "escalated" || !got.Time.Equal(ts) {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestRecordAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	r.Record(Transition{Service: "a"})
	r.Record(Transition{Service: "b"})
	r.Close()

	// Re-open (append) and add a third — append semantics across opens too.
	r2, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r2.Record(Transition{Service: "c"})
	r2.Close()

	f, _ := os.Open(path)
	defer f.Close()
	var svcs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var tr Transition
		if err := json.Unmarshal(sc.Bytes(), &tr); err != nil {
			t.Fatalf("Unmarshal line: %v", err)
		}
		svcs = append(svcs, tr.Service)
	}
	if got := strings.Join(svcs, ","); got != "a,b,c" {
		t.Errorf("append order = %q, want a,b,c", got)
	}
}

func TestRecordWriteFailureSurfaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}

	var buf bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&buf, nil))

	// Close the underlying file so the next Write fails — the failure must be
	// logged (surfaced), not silently dropped.
	r.f.Close()
	r.Record(Transition{Service: "youtube"})

	if !strings.Contains(buf.String(), "write failed") {
		t.Errorf("write failure not surfaced; log=%q", buf.String())
	}
}

func TestRecordConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := NewFileRecorder(path, nil)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	defer r.Close()

	const goroutines, perG = 4, 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				r.Record(Transition{Service: "svc"})
			}
		}()
	}
	wg.Wait()

	f, _ := os.Open(path)
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var tr Transition
		if err := json.Unmarshal(sc.Bytes(), &tr); err != nil {
			t.Fatalf("interleaved/corrupt line: %v (%q)", err, sc.Text())
		}
		n++
	}
	if n != goroutines*perG {
		t.Errorf("wrote %d lines, want %d", n, goroutines*perG)
	}
}

// The client needs the ring (for the UI) and a file (for anything investigated
// later) at the same time; the daemon's either/or could not say that.
func TestMultiFansOutAndTolerdatesNilAndEmpty(t *testing.T) {
	a, b := NewRingRecorder(8), NewRingRecorder(8)
	Multi(a, nil, b).Record(Transition{Service: "youtube"})
	for i, r := range []*RingRecorder{a, b} {
		if got := r.Snapshot(10, ""); len(got) != 1 || got[0].Service != "youtube" {
			t.Errorf("recorder %d got %v", i, got)
		}
	}
	// Empty and all-nil must be usable without a branch at the call site.
	Multi().Record(Transition{Service: "x"})
	Multi(nil, nil).Record(Transition{Service: "x"})
	// One recorder is returned unwrapped — no allocation for the common case.
	if _, wrapped := Multi(a).(multi); wrapped {
		t.Error("Multi wrapped a single recorder")
	}
}
