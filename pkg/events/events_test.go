package events

import "testing"

// TestNewBusChannelsBuffered guards the one behavioral claim in this package:
// NewBus returns three non-nil, buffered channels so a producer (prober, Brain,
// applier) does not block when no consumer is currently receiving. A send that
// would block on an unbuffered/nil channel fails the select's default arm.
func TestNewBusChannelsBuffered(t *testing.T) {
	b := NewBus()
	if b.Verdicts == nil || b.DesiredState == nil || b.ActualState == nil {
		t.Fatal("NewBus returned a nil channel")
	}

	// Each channel must accept at least one send without a receiver.
	select {
	case b.Verdicts <- ProductionVerdict{Service: "youtube"}:
	default:
		t.Error("Verdicts channel is not buffered")
	}
	select {
	case b.DesiredState <- DesiredStateChanged{Service: "youtube"}:
	default:
		t.Error("DesiredState channel is not buffered")
	}
	select {
	case b.ActualState <- ActualStateObserved{Service: "youtube"}:
	default:
		t.Error("ActualState channel is not buffered")
	}
}
