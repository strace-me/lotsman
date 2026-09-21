package spine

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/anomaly"
	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// Every hook must be present. This is the guard against the exact defect the
// package exists for: one root wiring a subset of the intelligence layer, which
// is invisible because each root tests only what it has.
func TestSmartsEveryHookPresent(t *testing.T) {
	s := Smarts(kb.New(), strategy.BuiltinCatalog(), brain.DefaultConfig(), []string{"youtube", "discord"})
	if s.FeedAnomaly == nil || s.ObserveHealth == nil || s.Threshold == nil ||
		s.Systemic == nil || s.FlapBackoff == nil || s.Anomaly == nil ||
		s.RecordSwitch == nil || s.SuggestClass == nil || s.BlockType == nil ||
		s.BlockTypesFor == nil {
		t.Fatalf("a Smarts hook is nil: %+v", s)
	}
}

// The systemic gate the office outage needed (LOT-74): most services down at once
// must read as one systemic event, not N independent failures to escalate.
func TestSmartsSystemic(t *testing.T) {
	s := Smarts(nil, nil, brain.DefaultConfig(), []string{"a", "b"})
	s.ObserveHealth("a", false)
	s.ObserveHealth("b", false)
	if !s.Systemic() {
		t.Error("2/2 services down must be systemic")
	}
	s.ObserveHealth("a", true)
	if s.Systemic() {
		t.Error("1/2 services down is not systemic (below the 60% fraction)")
	}
}

// A nil KB must not panic: degrade to the plain global threshold.
func TestSmartsNilKnowledgeDegrades(t *testing.T) {
	cfg := brain.DefaultConfig()
	s := Smarts(nil, nil, cfg, nil)
	if got := s.Threshold("youtube", "alt12"); got != cfg.EscalateFails {
		t.Errorf("nil KB: got %d, want the global threshold %d", got, cfg.EscalateFails)
	}
	if got := s.BlockTypesFor("alt12"); got != nil {
		t.Errorf("nil catalog: got %v, want nil", got)
	}
	if got := s.Anomaly("unknown"); got != anomaly.Healthy {
		t.Errorf("unknown service: got %q, want %q", got, anomaly.Healthy)
	}
}
