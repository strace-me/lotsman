package correlate

import "testing"

func TestSystemicNeedsMinServices(t *testing.T) {
	d := New(3, 0.6)
	d.Set("a", false)
	d.Set("b", false) // only 2 tracked, below min 3
	if d.Systemic() {
		t.Error("should not be systemic with fewer than min services")
	}
}

func TestSystemicWhenManyDown(t *testing.T) {
	d := New(3, 0.6)
	d.Set("youtube", false)
	d.Set("discord", false)
	d.Set("telegram", false)
	d.Set("github", true) // 3/4 down = 0.75 >= 0.6
	if !d.Systemic() {
		t.Errorf("3 of 4 down should be systemic")
	}
	down, total := d.Summary()
	if down != 3 || total != 4 {
		t.Errorf("summary = %d/%d, want 3/4", down, total)
	}
}

func TestLocalFailureNotSystemic(t *testing.T) {
	d := New(3, 0.6)
	d.Set("youtube", false) // only one down
	d.Set("discord", true)
	d.Set("telegram", true)
	d.Set("github", true) // 1/4 = 0.25 < 0.6
	if d.Systemic() {
		t.Error("a single service down must not read as systemic")
	}
}

func TestRecoveryClearsSystemic(t *testing.T) {
	d := New(2, 0.6)
	d.Set("a", false)
	d.Set("b", false)
	if !d.Systemic() {
		t.Fatal("setup: should be systemic")
	}
	d.Set("a", true)
	d.Set("b", true)
	if d.Systemic() {
		t.Error("should clear once services recover")
	}
}
