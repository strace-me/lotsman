package balancer

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/quality"
)

func q(p95, jitter, loss float64, samples int) quality.Quality {
	return quality.Quality{P95ms: p95, JitterMs: jitter, Loss: loss, Samples: samples}
}

func TestVoicePrefersLowJitter(t *testing.T) {
	// A is faster on average but jittery; B is a touch slower but stable.
	// For voice, B should win (jitter weighted heavily).
	a := Candidate{ID: "fast-jittery", Q: q(40, 60, 0.0, 30)}
	b := Candidate{ID: "stable", Q: q(60, 5, 0.0, 30)}
	ranked := Rank([]Candidate{a, b}, ProfileFor("voice"))
	if ranked[0].ID != "stable" {
		t.Errorf("voice ranked %q first, want stable (low jitter)", ranked[0].ID)
	}
}

func TestGeneralPrefersFastWhenStable(t *testing.T) {
	a := Candidate{ID: "fast", Q: q(20, 5, 0.0, 30)}
	b := Candidate{ID: "slow", Q: q(200, 5, 0.0, 30)}
	ranked := Rank([]Candidate{b, a}, ProfileFor("streaming"))
	if ranked[0].ID != "fast" {
		t.Errorf("ranked %q first, want fast", ranked[0].ID)
	}
}

func TestStreamingPrefersBandwidth(t *testing.T) {
	// Same latency; one has much higher bandwidth.
	a := Candidate{ID: "lowbw", Q: q(50, 5, 0, 30), BandwidthMbps: 10}
	b := Candidate{ID: "highbw", Q: q(55, 5, 0, 30), BandwidthMbps: 200}
	ranked := Rank([]Candidate{a, b}, ProfileFor("streaming"))
	if ranked[0].ID != "highbw" {
		t.Errorf("streaming ranked %q first, want highbw", ranked[0].ID)
	}
}

func TestLossDominatesForVoice(t *testing.T) {
	a := Candidate{ID: "fast-lossy", Q: q(20, 5, 0.15, 30)}
	b := Candidate{ID: "clean", Q: q(70, 8, 0.0, 30)}
	ranked := Rank([]Candidate{a, b}, ProfileFor("voice"))
	if ranked[0].ID != "clean" {
		t.Errorf("voice ranked %q first, want clean (no loss)", ranked[0].ID)
	}
}

func TestUnknownMetricsIgnored(t *testing.T) {
	// No samples at all -> score 0, ranks last; a node with data wins.
	none := Candidate{ID: "none"}
	good := Candidate{ID: "good", Q: q(30, 5, 0, 20)}
	ranked := Rank([]Candidate{none, good}, ProfileFor("general"))
	if ranked[0].ID != "good" {
		t.Errorf("ranked %q first, want good (none has no data)", ranked[0].ID)
	}
	if Score(none, ProfileFor("general")) != 0 {
		t.Errorf("no-data score = %v, want 0", Score(none, ProfileFor("general")))
	}
}
