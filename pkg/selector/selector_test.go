package selector

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/tspu"
)

func TestEmpty(t *testing.T) {
	if _, ok := Select(nil, "", func(string) float64 { return 0 }); ok {
		t.Error("empty candidates should return ok=false")
	}
}

func TestPrefersClassThenScore(t *testing.T) {
	cands := []Candidate{
		{StrategyID: "vpn1", Class: strategy.ClassVPN},
		{StrategyID: "zap_lo", Class: strategy.ClassZapret},
		{StrategyID: "zap_hi", Class: strategy.ClassZapret},
	}
	score := map[string]float64{"vpn1": 0.99, "zap_lo": 0.3, "zap_hi": 0.7}

	// DPI reset -> prefer zapret class; within it, the higher score wins
	// (even though vpn1 scores highest overall).
	got, ok := Select(cands, tspu.SuggestClass(tspu.TCPReset), func(id string) float64 { return score[id] })
	if !ok || got.StrategyID != "zap_hi" {
		t.Fatalf("got %+v, want zap_hi", got)
	}
}

func TestFallsBackWhenNoClassMatch(t *testing.T) {
	cands := []Candidate{
		{StrategyID: "vpn1", Class: strategy.ClassVPN},
		{StrategyID: "vpn2", Class: strategy.ClassVPN},
	}
	score := map[string]float64{"vpn1": 0.4, "vpn2": 0.8}
	// Prefer zapret, but none available -> best overall (vpn2).
	got, _ := Select(cands, strategy.ClassZapret, func(id string) float64 { return score[id] })
	if got.StrategyID != "vpn2" {
		t.Errorf("got %+v, want vpn2 fallback", got)
	}
}

// End-to-end with a real KB: a fast working strategy beats a slow one.
func TestWithKBScoreFavorsFastWorking(t *testing.T) {
	k := kb.New()
	for i := 0; i < 30; i++ {
		k.RecordOutcome("yt", "fast", true, 20)  // works, 20ms
		k.RecordOutcome("yt", "slow", true, 400) // works, 400ms
	}
	cands := []Candidate{
		{StrategyID: "fast", Class: strategy.ClassZapret},
		{StrategyID: "slow", Class: strategy.ClassZapret},
	}
	got, _ := Select(cands, strategy.ClassZapret, func(id string) float64 { return k.Score("yt", id) })
	if got.StrategyID != "fast" {
		t.Errorf("got %+v, want fast (lower latency)", got)
	}
}
