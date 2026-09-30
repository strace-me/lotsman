package rulesets

import (
	"context"
	"testing"
)

// The whole point of pinning is that it does not depend on the API. It used to call
// releases/latest anyway, so a rate limit (403, unauthenticated limit is 60/h) broke
// the updater for a tag it already had. A releaser that fails every call is the
// observable version of "the pin must not need it".
type deadReleaser struct{ calls int }

func (d *deadReleaser) Latest(ctx context.Context, repo string) (string, error) {
	d.calls++
	return "", errRateLimited
}
func (d *deadReleaser) Fetch(ctx context.Context, repo, tag string) (string, func(), error) {
	return "", func() {}, errRateLimited
}

func TestPinnedUpdaterNeverTouchesTheAPI(t *testing.T) {
	r := &deadReleaser{}
	u := &Updater{Repo: "r/r", Pin: "v2026-09-01", LiveDir: t.TempDir(),
		PathFor: func(tag string) (string, bool) { return "rule-set-geosite/" + tag + ".srs", true }, Counter: noCounter, Releaser: r, Log: quietLogger()}
	// The fetch is expected to fail here — there is no network in a test. What must
	// NOT happen is a call to Latest, because the pin already named the release.
	_ = u.Update(context.Background())
	if r.calls != 0 {
		t.Errorf("Latest called %d times under a pin; pinning must not need the API", r.calls)
	}
	if u.pin != "v2026-09-01" {
		t.Errorf("pin = %q, want it left alone at v2026-09-01", u.pin)
	}
}

// The flip side: with no pin, or with AutoBump, latest IS the point, so it must be
// asked for. A guard that quietly stopped updating would be worse than the bug.
func TestUnpinnedUpdaterStillResolvesLatest(t *testing.T) {
	r := &deadReleaser{}
	u := &Updater{Repo: "r/r", LiveDir: t.TempDir(), PathFor: func(tag string) (string, bool) { return "rule-set-geosite/" + tag + ".srs", true },
		Counter: noCounter, Releaser: r, Log: quietLogger()}
	_ = u.Update(context.Background())
	if r.calls == 0 {
		t.Error("no pin and AutoBump off, yet latest was never resolved — updates are dead")
	}
}
