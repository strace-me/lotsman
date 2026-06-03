package policy

import (
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/anomaly"
)

func TestHealthyStays(t *testing.T) {
	d := Decide(Inputs{ProbeOK: true, Anomaly: anomaly.Healthy})
	if d.Action != Stay || d.Reason != "healthy" {
		t.Errorf("got %+v", d)
	}
}

func TestDegradedButReachableStaysWithWatch(t *testing.T) {
	d := Decide(Inputs{ProbeOK: true, Anomaly: anomaly.Degraded})
	if d.Action != Stay || d.Reason != "degraded_watch" {
		t.Errorf("got %+v", d)
	}
}

func TestSystemicSuppressesEscalation(t *testing.T) {
	// Even past the fail threshold, a systemic outage must NOT escalate.
	d := Decide(Inputs{ProbeOK: false, Systemic: true, ConsecutiveFails: 9, EscalateAt: 3})
	if d.Action != Hold || d.Reason != "systemic_outage" {
		t.Errorf("got %+v, want hold/systemic_outage", d)
	}
}

func TestSettlingHolds(t *testing.T) {
	d := Decide(Inputs{ProbeOK: false, InSettling: true, ConsecutiveFails: 9, EscalateAt: 3})
	if d.Action != Hold || d.Reason != "settling_window" {
		t.Errorf("got %+v", d)
	}
}

func TestFlapBackoffHolds(t *testing.T) {
	d := Decide(Inputs{ProbeOK: false, FlapBackoff: 30 * time.Second, ConsecutiveFails: 9, EscalateAt: 3})
	if d.Action != Hold || d.Reason != "flap_backoff" {
		t.Errorf("got %+v", d)
	}
}

func TestEscalatesAtThreshold(t *testing.T) {
	d := Decide(Inputs{ProbeOK: false, ConsecutiveFails: 3, EscalateAt: 3})
	if d.Action != Escalate || d.Reason != "fails_threshold" {
		t.Errorf("got %+v, want escalate", d)
	}
}

func TestHoldsBelowThreshold(t *testing.T) {
	d := Decide(Inputs{ProbeOK: false, ConsecutiveFails: 1, EscalateAt: 3})
	if d.Action != Hold || d.Reason != "accumulating_fails" {
		t.Errorf("got %+v", d)
	}
}

// Priority: systemic beats settling beats flap beats threshold.
func TestSignalPriority(t *testing.T) {
	d := Decide(Inputs{ProbeOK: false, Systemic: true, InSettling: true, FlapBackoff: time.Minute, ConsecutiveFails: 9, EscalateAt: 3})
	if d.Reason != "systemic_outage" {
		t.Errorf("systemic should win, got %q", d.Reason)
	}
}
