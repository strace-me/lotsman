package scoring

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/selector"
	"github.com/strace-me/lotsman/pkg/strategy"
)

func TestProfileFor(t *testing.T) {
	if ProfileFor("voice") != Voice || ProfileFor("streaming") != Streaming || ProfileFor("messaging") != General {
		t.Error("ProfileFor mapping wrong")
	}
}

func TestVoiceWeighsJitter(t *testing.T) {
	// Two strategies, same success and latency, different jitter.
	steady := kb.Stats{Success: 0.95, RTTms: 60, JitterMs: 5}
	jittery := kb.Stats{Success: 0.95, RTTms: 60, JitterMs: 150}

	// Under the Voice profile, the steady one must score clearly higher.
	if Score(steady, Voice) <= Score(jittery, Voice) {
		t.Errorf("voice: steady (%.3f) should beat jittery (%.3f)", Score(steady, Voice), Score(jittery, Voice))
	}
	// Under Streaming (low jitter weight) the gap should be small/negligible.
	gapVoice := Score(steady, Voice) - Score(jittery, Voice)
	gapStream := Score(steady, Streaming) - Score(jittery, Streaming)
	if gapStream >= gapVoice {
		t.Errorf("jitter should matter more for voice (%.3f) than streaming (%.3f)", gapVoice, gapStream)
	}
}

func TestStreamingWeighsSuccessOverLatency(t *testing.T) {
	reliable := kb.Stats{Success: 0.98, RTTms: 300, JitterMs: 10}
	flaky := kb.Stats{Success: 0.70, RTTms: 30, JitterMs: 10}
	if Score(reliable, Streaming) <= Score(flaky, Streaming) {
		t.Error("streaming should prefer reliable-but-slower over fast-but-flaky")
	}
}

// ScorerFor integrates with the selector and a real KB.
func TestScorerForWithSelector(t *testing.T) {
	k := kb.New()
	for i := 0; i < 30; i++ {
		k.RecordOutcome("disc", "steady", true, 60)
		// jittery: alternate 60/260 to build jitter on "rough"
		rtt := 60
		if i%2 == 0 {
			rtt = 260
		}
		k.RecordOutcome("disc", "rough", true, rtt)
	}
	cands := []selector.Candidate{
		{StrategyID: "steady", Class: strategy.ClassZapret},
		{StrategyID: "rough", Class: strategy.ClassZapret},
	}
	got, _ := selector.Select(cands, strategy.ClassZapret, ScorerFor(k, "disc", "voice"))
	if got.StrategyID != "steady" {
		t.Errorf("voice selector picked %q, want steady (lower jitter)", got.StrategyID)
	}
}
