package dataplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClashConnections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"connections":[
			{"chains":["hysteria2","sel-youtube"],"upload":10,"download":20,"rule":"route(sel-youtube)",
			 "metadata":{"host":"x.googlevideo.com","destinationIP":"1.2.3.4","destinationPort":"443","network":"udp"}}
		]}`))
	}))
	defer srv.Close()

	conns, err := NewClashClient(srv.URL, "").Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("got %d conns, want 1", len(conns))
	}
	c := conns[0]
	if c.FinalOutbound() != "hysteria2" {
		t.Errorf("FinalOutbound=%q, want hysteria2", c.FinalOutbound())
	}
	if c.Upload != 10 || c.Download != 20 || c.Metadata.Network != "udp" || c.Metadata.DestinationIP != "1.2.3.4" {
		t.Errorf("decoded conn wrong: %+v", c)
	}
}

func TestClashConnectionsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := NewClashClient(srv.URL, "").Connections(context.Background()); err == nil {
		t.Error("non-200 /connections must error")
	}
}

func TestClashProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"type":"Selector","now":"de","all":["de","nl"]}`))
	}))
	defer srv.Close()
	pi, err := NewClashClient(srv.URL, "").Proxy(context.Background(), "sel-youtube")
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if pi.Type != "Selector" || pi.Now != "de" || len(pi.All) != 2 {
		t.Errorf("ProxyInfo wrong: %+v", pi)
	}
}

func TestConnectionFinalOutbound(t *testing.T) {
	if got := (Connection{Chains: []string{"a", "b"}}).FinalOutbound(); got != "a" {
		t.Errorf("FinalOutbound=%q, want a", got)
	}
	if got := (Connection{}).FinalOutbound(); got != "" {
		t.Errorf("empty chains FinalOutbound=%q, want empty", got)
	}
}

func TestHTTPProberReachableAnyStatus(t *testing.T) {
	// Any HTTP response (even 500) means the path is reachable -> OK.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := NewHTTPProber(map[string]string{"youtube": srv.URL})
	if v := p.Probe(context.Background(), "youtube", 0); !v.OK {
		t.Errorf("reachable target must be OK, got %+v", v)
	}
	// No probe target for the service -> not OK.
	if v := p.Probe(context.Background(), "unknown", 0); v.OK {
		t.Errorf("missing target must be not-OK, got %+v", v)
	}
}

func TestHTTPProberUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now refuses connections
	p := NewHTTPProber(map[string]string{"youtube": url})
	if v := p.Probe(context.Background(), "youtube", 0); v.OK || v.Err == "" {
		t.Errorf("unreachable target must be not-OK with an error, got %+v", v)
	}
}

func TestFuncProber(t *testing.T) {
	fp := NewFuncProber(func(service string, position int, _ time.Duration) (bool, int) {
		return position == 0, 42
	})
	if v := fp.Probe(context.Background(), "youtube", 0); !v.OK || v.RTTms != 42 || v.Service != "youtube" {
		t.Errorf("pos0 verdict wrong: %+v", v)
	}
	if v := fp.Probe(context.Background(), "youtube", 1); v.OK {
		t.Errorf("pos1 should be not-OK per fn, got %+v", v)
	}
}
