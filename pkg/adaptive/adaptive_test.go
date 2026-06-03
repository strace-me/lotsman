package adaptive

import "testing"

func base() *Tuner { return NewTuner(Thresholds{EscalateAt: 3, RecoverAt: 5}) }

func TestReliableServiceKeepsBase(t *testing.T) {
	got := base().Tune(1.0, 0)
	if got.EscalateAt != 3 || got.RecoverAt != 5 {
		t.Errorf("reliable = %+v, want 3/5", got)
	}
}

func TestFlakyServiceEscalatesSlower(t *testing.T) {
	got := base().Tune(0.0, 0) // fully unreliable
	if got.EscalateAt != 7 {   // 3 + spread 4
		t.Errorf("flaky escalate = %d, want 7", got.EscalateAt)
	}
	half := base().Tune(0.5, 0)
	if half.EscalateAt != 5 { // 3 + round(0.5*4)=2
		t.Errorf("half escalate = %d, want 5", half.EscalateAt)
	}
}

func TestFlappingDemandsMoreRecovery(t *testing.T) {
	got := base().Tune(1.0, 3) // reliable but flapping 3x
	if got.RecoverAt != 8 {    // 5 + 3
		t.Errorf("flapping recover = %d, want 8", got.RecoverAt)
	}
}

func TestRecoverSpreadCapped(t *testing.T) {
	got := base().Tune(0.0, 100) // extreme flapping + unreliable
	if got.RecoverAt != 5+6 {     // capped at base + recoverSpread(6)
		t.Errorf("capped recover = %d, want 11", got.RecoverAt)
	}
}

func TestReliabilityClamped(t *testing.T) {
	if got := base().Tune(2.0, 0); got.EscalateAt != 3 {
		t.Errorf("reliability>1 should clamp: %+v", got)
	}
	if got := base().Tune(-1, 0); got.EscalateAt != 7 {
		t.Errorf("reliability<0 should clamp to 0 (escalate 7): %+v", got)
	}
}
