package core

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

// A target marked h3:// must be pulled over QUIC and an ordinary one over TCP.
// The whole point is that a recipe's udp/443 profile — YouTube composes one, with
// a QUIC fake in it — has never been measured by anything in this project.
func TestEndpointsPickTheTransportPerTarget(t *testing.T) {
	tcp, h3 := &http.Client{}, &http.Client{}
	tcpEps, h3Eps := splitEndpoints([]string{"https://www.youtube.com/", "h3://www.youtube.com/"}, tcp, h3)
	if len(tcpEps) != 1 || len(h3Eps) != 1 {
		t.Fatalf("groups not split: tcp=%d quic=%d", len(tcpEps), len(h3Eps))
	}
	if tcpEps[0].Client != tcp || tcpEps[0].URL != "https://www.youtube.com/" {
		t.Errorf("plain target did not go over TCP: %+v", tcpEps[0])
	}
	if h3Eps[0].Client != h3 {
		t.Errorf("h3 target did not go over QUIC: %+v", h3Eps[0])
	}
	if h3Eps[0].URL != "https://www.youtube.com/" {
		t.Errorf("the marker must be translated back to https for the request, got %q", h3Eps[0].URL)
	}
}

// A lane that cannot isolate QUIC drops the h3 targets rather than pulling them
// over TCP. Substituting the transport would not be a weaker measurement, it
// would be a different one filed under the same name — and the verdict decides
// what real traffic runs.
func TestEndpointsDropH3WhenItCannotBeIsolated(t *testing.T) {
	tcp := &http.Client{}
	tcpEps, h3Eps := splitEndpoints([]string{"h3://www.youtube.com/", "https://www.youtube.com/"}, tcp, nil)
	if len(h3Eps) != 0 {
		t.Fatalf("a lane that cannot isolate QUIC must not pull it: %+v", h3Eps)
	}
	if len(tcpEps) != 1 || tcpEps[0].Client != tcp || tcpEps[0].URL != "https://www.youtube.com/" {
		t.Errorf("the surviving endpoint is wrong: %+v", tcpEps)
	}
}

// A rule whose only volume target is a QUIC one is still judgeable — otherwise a
// service measured the way it is actually used would look unmeasurable.
func TestAH3OnlyRuleIsJudgeable(t *testing.T) {
	c := &Core{}
	if !c.canJudge(registry.Service{VolumeTarget: "h3://www.youtube.com/"}) {
		t.Error("a rule with an h3 volume target reads as having none")
	}
	if got := volumeTargets(registry.Service{VolumeTarget: "h3://www.youtube.com/"}); len(got) != 1 {
		t.Errorf("volumeTargets dropped the h3 target: %v", got)
	}
	if c.canJudge(registry.Service{ProbeTarget: "https://discord.com/api/v9/gateway"}) {
		t.Error("a probe target is not a volume target; judging by it is how the KB once scored every recipe zero")
	}
}

type deadTransport struct{}

func (deadTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errDeadQUIC
}

var errDeadQUIC = errors.New("quic: timeout: no recent network activity")

// Worst-of across DOMAINS, best-of across TRANSPORTS — and this is the half that
// could quietly pin YouTube to the tunnel forever if it were wrong. A browser
// tries QUIC, gets nothing, falls back to TCP and plays the video. Condemning the
// recipe because the half the browser abandoned did not carry would be a verdict
// about a path nobody uses when it is broken.
func TestADeadQuicHalfDoesNotCondemnAWorkingTCPOne(t *testing.T) {
	body := strings.Repeat("y", 128<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := &Core{log: slog.New(slog.DiscardHandler)}
	svc := registry.Service{
		Name:         "youtube",
		VolumeBytes:  8 << 10,
		VolumeTarget: srv.URL + "/page",
		// The marker is translated to https:// and never reached: the client below
		// refuses every request, which is exactly a blocked QUIC path.
		VolumeTargets: []string{"h3://www.youtube.com/"},
	}
	ok, why, measured, detail := c.measureVolume(context.Background(), svc,
		srv.Client(), &http.Client{Transport: deadTransport{}})
	if !ok {
		t.Fatalf("TCP carried the volume; the recipe must pass. why=%q detail=%v", why, detail)
	}
	if !measured {
		t.Error("both halves were actually asked; the pass must count as measured")
	}
	// And the dead half must still be visible, because QUIC that is throttled
	// rather than dropped is a video that stalls, and only the log shows it.
	if len(detail) != 2 || !strings.Contains(strings.Join(detail, " "), "quic") {
		t.Errorf("the QUIC reading is missing from the record: %v", detail)
	}
}

// The mirror: nothing carried, so the recipe is condemned — with both transports
// named, not one.
func TestBothTransportsDeadIsAFailureThatNamesBoth(t *testing.T) {
	c := &Core{log: slog.New(slog.DiscardHandler)}
	svc := registry.Service{
		Name:          "youtube",
		VolumeBytes:   8 << 10,
		VolumeTarget:  "https://www.youtube.com/",
		VolumeTargets: []string{"h3://www.youtube.com/"},
	}
	dead := &http.Client{Transport: deadTransport{}}
	ok, why, measured, _ := c.measureVolume(context.Background(), svc, dead, dead)
	if ok {
		t.Fatal("nothing carried and the recipe passed")
	}
	if !measured {
		t.Fatal("both halves were asked and answered; that is a measurement")
	}
	if !strings.Contains(why, "tcp") || !strings.Contains(why, "quic") {
		t.Errorf("the reason must name both transports, got %q", why)
	}
}
