package burstprobe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each endpoint must be pulled with ITS OWN client. A service is not carried by
// one transport — YouTube's media is QUIC and its page is TCP — and one client
// for both measures one profile while reporting on two.
func TestProbeEndpointsUsesThePerEndpointClient(t *testing.T) {
	body := strings.Repeat("x", 4096)
	var viaA, viaB int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Which") {
		case "a":
			viaA++
		case "b":
			viaB++
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	mark := func(which string) *http.Client {
		return &http.Client{Transport: headerRT{which: which, base: http.DefaultTransport}}
	}
	q := ProbeEndpoints(context.Background(), []Endpoint{
		{URL: srv.URL + "/one", Client: mark("a")},
		{URL: srv.URL + "/two", Client: mark("b")},
	}, 1024, 1)

	if viaA != 1 || viaB != 1 {
		t.Fatalf("each endpoint must be fetched by its own client: a=%d b=%d", viaA, viaB)
	}
	if q.Samples == 0 {
		t.Error("no samples recorded")
	}
}

type headerRT struct {
	which string
	base  http.RoundTripper
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Which", h.which)
	return h.base.RoundTrip(r)
}

// Zero bytes is where two different failures meet. A SYN nobody answered means a
// desync recipe cannot help — there will be no ClientHello for it to rewrite —
// while a hello swallowed on an established connection is exactly what a recipe
// is for. The probe held that sentence and dropped it, and both readings were
// then argued from a byte count of zero.
func TestTheProbeCarriesTheTransportsOwnComplaint(t *testing.T) {
	dead := &http.Client{Transport: errRT{}}
	q, said := ProbeEndpointsSaying(context.Background(),
		[]Endpoint{{URL: "https://example.invalid/", Client: dead}}, 1024, 2)
	if q.Bytes != 0 {
		t.Fatalf("nothing was delivered, got %d bytes", q.Bytes)
	}
	if !strings.Contains(said, "dial tcp") {
		t.Errorf("the reason the fetch failed was dropped, got %q", said)
	}
}

type errRT struct{}

func (errRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errFakeDial
}

var errFakeDial = errors.New("dial tcp 142.251.157.4:443: i/o timeout")
