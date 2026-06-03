package subscription

import (
	"strconv"
	"strings"
	"time"
)

// Userinfo is the traffic/expiry data many providers return in the
// "Subscription-Userinfo" response header:
//
//	upload=455727; download=2787212; total=10737418240; expire=1740268800
//
// It lets Lotsman warn before a subscription runs out of quota or expires.
type Userinfo struct {
	Upload   int64     // bytes uploaded
	Download int64     // bytes downloaded
	Total    int64     // bytes quota (0 = unknown/unlimited)
	Expire   time.Time // zero if not provided
}

// ParseUserinfo parses a Subscription-Userinfo header value. ok is false if the
// header carried none of the recognized fields.
func ParseUserinfo(header string) (u Userinfo, ok bool) {
	for _, part := range strings.Split(header, ";") {
		k, v, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			u.Upload, ok = n, true
		case "download":
			u.Download, ok = n, true
		case "total":
			u.Total, ok = n, true
		case "expire":
			if n > 0 {
				u.Expire, ok = time.Unix(n, 0).UTC(), true
			}
		}
	}
	return u, ok
}

// Used returns bytes consumed (upload + download).
func (u Userinfo) Used() int64 { return u.Upload + u.Download }

// Remaining returns bytes left in quota, or -1 if total is unknown.
func (u Userinfo) Remaining() int64 {
	if u.Total <= 0 {
		return -1
	}
	r := u.Total - u.Used()
	if r < 0 {
		return 0
	}
	return r
}

// FractionUsed returns used/total in [0,1], or -1 if total is unknown.
func (u Userinfo) FractionUsed() float64 {
	if u.Total <= 0 {
		return -1
	}
	f := float64(u.Used()) / float64(u.Total)
	if f > 1 {
		return 1
	}
	return f
}

// DaysUntilExpire returns days until expiry relative to now, or -1 if no expiry
// is set. Negative means already expired.
func (u Userinfo) DaysUntilExpire(now time.Time) float64 {
	if u.Expire.IsZero() {
		return -1
	}
	return u.Expire.Sub(now).Hours() / 24
}

// Expired reports whether the subscription's expiry is in the past.
func (u Userinfo) Expired(now time.Time) bool {
	return !u.Expire.IsZero() && now.After(u.Expire)
}
