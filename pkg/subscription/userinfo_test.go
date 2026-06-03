package subscription

import (
	"testing"
	"time"
)

func TestParseUserinfo(t *testing.T) {
	u, ok := ParseUserinfo("upload=455727; download=2787212; total=10737418240; expire=1740268800")
	if !ok {
		t.Fatal("expected ok")
	}
	if u.Upload != 455727 || u.Download != 2787212 || u.Total != 10737418240 {
		t.Errorf("parsed wrong: %+v", u)
	}
	if u.Used() != 455727+2787212 {
		t.Errorf("used = %d", u.Used())
	}
	if u.Expire.Unix() != 1740268800 {
		t.Errorf("expire = %v", u.Expire)
	}
}

func TestUserinfoQuotaMath(t *testing.T) {
	u := Userinfo{Upload: 1, Download: 1, Total: 10}
	if u.Remaining() != 8 {
		t.Errorf("remaining = %d, want 8", u.Remaining())
	}
	if f := u.FractionUsed(); f < 0.19 || f > 0.21 {
		t.Errorf("fraction = %v, want ~0.2", f)
	}

	// Over quota clamps to 0 remaining / 1.0 used.
	over := Userinfo{Download: 20, Total: 10}
	if over.Remaining() != 0 || over.FractionUsed() != 1 {
		t.Errorf("over quota: remaining=%d frac=%v", over.Remaining(), over.FractionUsed())
	}

	// Unknown total.
	unk := Userinfo{Download: 5}
	if unk.Remaining() != -1 || unk.FractionUsed() != -1 {
		t.Errorf("unknown total: remaining=%d frac=%v", unk.Remaining(), unk.FractionUsed())
	}
}

func TestUserinfoExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	future := Userinfo{Expire: now.Add(48 * time.Hour)}
	if d := future.DaysUntilExpire(now); d < 1.9 || d > 2.1 {
		t.Errorf("days = %v, want ~2", d)
	}
	if future.Expired(now) {
		t.Error("future should not be expired")
	}

	past := Userinfo{Expire: now.Add(-time.Hour)}
	if !past.Expired(now) {
		t.Error("past should be expired")
	}
	if d := past.DaysUntilExpire(now); d >= 0 {
		t.Errorf("expired days = %v, want negative", d)
	}

	none := Userinfo{}
	if none.DaysUntilExpire(now) != -1 || none.Expired(now) {
		t.Error("no-expiry handling wrong")
	}
}

func TestParseUserinfoEmpty(t *testing.T) {
	if _, ok := ParseUserinfo("garbage without fields"); ok {
		t.Error("expected ok=false for header with no recognized fields")
	}
}
