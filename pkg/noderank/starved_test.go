package noderank

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/balancer"
)

// The overnight run emitted 58 "all eligible nodes down" and EVERY one had
// eligible=1. Read literally that says 94 nodes died, and it sent me hunting for
// a screening bug that does not run in production at all (nothing sets
// ScreenURL/ScreenTimeout, so screen() returns true immediately). The truth was
// duller: narrowing by country, pool and selector had left ONE candidate, and it
// failed its probe. There was no ranking to get wrong — nothing to rank.
//
// These three are different events. The log has to name which one happened, and
// it has to put the count where it cannot be skimmed past.
func TestPoolEmptinessNamesItsCause(t *testing.T) {
	cases := []struct {
		name     string
		cands    []Candidate
		wantMsg  string
		wantNote string
	}{
		{
			name:     "one candidate, one failure — a starved set, not an outage",
			cands:    []Candidate{{"de-1", "de"}},
			wantMsg:  "the only eligible candidate failed",
			wantNote: "widening",
		},
		{
			// An empty set never reaches the pool: country narrowing catches it first,
			// and that message is better than anything this site could say. Asserted
			// here so a future "harmless" extra branch does not quietly shadow it.
			name:    "nothing survived narrowing — caught earlier, with better detail",
			cands:   []Candidate{{"ru-1", "ru"}},
			wantMsg: "no country-eligible nodes",
		},
		{
			name:     "three candidates all down — a real outage",
			cands:    []Candidate{{"de-1", "de"}, {"nl-1", "nl"}, {"pl-1", "pl"}},
			wantMsg:  "all eligible nodes down",
			wantNote: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fakeAPI with no delays answers every NodeDelay as a failure, which
			// is the state a dead node is in.
			var buf bytes.Buffer
			members := make([]string, 0, len(tc.cands))
			for _, c := range tc.cands {
				members = append(members, c.Tag)
			}
			r := New(newFake(members[0], members, map[string]map[string]int{}), 1, false,
				slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			svc := Service{Name: "s", Selector: "sel-s", ProbeURL: "http://svc.invalid",
				Weights: balancer.ProfileFor("streaming"), ExcludeCC: []string{"ru"}}
			if _, err := r.Pick(context.Background(), svc, tc.cands); err != nil {
				t.Fatalf("Pick: %v", err)
			}
			line := ""
			for _, l := range strings.Split(buf.String(), "\n") {
				if strings.Contains(l, "noderank:") {
					line = l
				}
			}
			if line == "" {
				t.Fatal("an empty pool logged nothing at all")
			}
			if !strings.Contains(line, tc.wantMsg) {
				t.Errorf("message = %q, want it to name the case: %q", line, tc.wantMsg)
			}
			if tc.wantNote != "" && !strings.Contains(line, tc.wantNote) {
				t.Errorf("message = %q, want the note %q so the fix is obvious", line, tc.wantNote)
			}
			// The count must be on the line, and it must be readable without counting
			// words: this run's whole problem was that eligible=1 was buried behind
			// the service name.
			if !strings.Contains(line, "candidates=") && !strings.Contains(line, "eligible=") {
				t.Errorf("message = %q, carries no candidate count at all", line)
			}
		})
	}
}

// A healthy candidate set must NOT produce any of the three warnings, or the log
// teaches the operator to ignore them.
func TestHealthySetIsSilent(t *testing.T) {
	var buf bytes.Buffer
	api := newFake("de-1", []string{"de-1", "nl-1"}, map[string]map[string]int{
		"de-1": {"http://svc.invalid": 40}, "nl-1": {"http://svc.invalid": 60}})
	r := New(api, 1, false, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	svc := Service{Name: "s", Selector: "sel-s", ProbeURL: "http://svc.invalid",
		Weights: balancer.ProfileFor("streaming"), ExcludeCC: []string{"ru"}}
	if _, err := r.Pick(context.Background(), svc, []Candidate{{"de-1", "de"}, {"nl-1", "nl"}}); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if strings.Contains(buf.String(), "noderank:") {
		t.Errorf("a healthy set warned: %s", buf.String())
	}
}
