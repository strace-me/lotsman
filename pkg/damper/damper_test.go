package damper

import (
	"testing"
	"time"
)

func TestNoBackoffWithinFreeLimit(t *testing.T) {
	d := New(time.Minute, 3, 10*time.Second, 5*time.Minute)
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		d.Record("discord", now.Add(time.Duration(i)*time.Second))
	}
	if b := d.Backoff("discord", now.Add(3*time.Second)); b != 0 {
		t.Errorf("backoff = %v, want 0 within free limit", b)
	}
}

func TestExponentialBackoff(t *testing.T) {
	d := New(time.Minute, 3, 10*time.Second, 5*time.Minute)
	now := time.Unix(1_700_000_000, 0)
	rec := func(n int) {
		for i := 0; i < n; i++ {
			d.Record("discord", now.Add(time.Duration(i)*time.Second))
		}
	}
	rec(4) // 1 excess -> base
	if b := d.Backoff("discord", now.Add(4*time.Second)); b != 10*time.Second {
		t.Errorf("4 flips: backoff = %v, want 10s", b)
	}

	d2 := New(time.Minute, 3, 10*time.Second, 5*time.Minute)
	for i := 0; i < 5; i++ {
		d2.Record("x", now.Add(time.Duration(i)*time.Second))
	}
	if b := d2.Backoff("x", now.Add(5*time.Second)); b != 20*time.Second { // 2 excess -> 2*base
		t.Errorf("5 flips: backoff = %v, want 20s", b)
	}
}

func TestBackoffCappedAtMax(t *testing.T) {
	d := New(time.Hour, 1, 10*time.Second, 40*time.Second)
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 10; i++ {
		d.Record("x", now.Add(time.Duration(i)*time.Second))
	}
	if b := d.Backoff("x", now.Add(10*time.Second)); b != 40*time.Second {
		t.Errorf("many flips: backoff = %v, want cap 40s", b)
	}
}

func TestOldTransitionsPruned(t *testing.T) {
	d := New(time.Minute, 2, 10*time.Second, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	// 4 flips but spread so only the last 2 are within the window.
	d.Record("x", now)
	d.Record("x", now.Add(10*time.Second))
	d.Record("x", now.Add(2*time.Minute))
	d.Record("x", now.Add(2*time.Minute+10*time.Second))
	// At now+2m10s, only 2 events are within the last minute -> within free limit.
	if b := d.Backoff("x", now.Add(2*time.Minute+10*time.Second)); b != 0 {
		t.Errorf("backoff = %v, want 0 (old flips pruned)", b)
	}
}
