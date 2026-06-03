package dataplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClashSetSelectorAndEnsurePool(t *testing.T) {
	var gotPath, gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies/vpn_url_test":
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"name":"vpn_url_test","type":"URLTest"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/vpn-pool":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			gotPath, gotName = r.URL.Path, body["name"]
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClashClient(srv.URL, "")
	ctx := context.Background()

	if err := c.EnsurePool(ctx, "vpn_url_test"); err != nil {
		t.Fatalf("EnsurePool: %v", err)
	}
	if err := c.EnsurePool(ctx, "missing"); err == nil {
		t.Error("EnsurePool(missing) should error on 404")
	}

	if err := c.SetSelector(ctx, "vpn-pool", "vpn_url_test"); err != nil {
		t.Fatalf("SetSelector: %v", err)
	}
	if gotPath != "/proxies/vpn-pool" || gotName != "vpn_url_test" {
		t.Errorf("selector PUT path=%q name=%q", gotPath, gotName)
	}
}

func TestClashNodeDelayMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"delay": not-json`) // truncated/invalid body
	}))
	defer srv.Close()

	c := NewClashClient(srv.URL, "")
	d, err := c.NodeDelay(context.Background(), "node-1", "http://x", time.Second)
	if err == nil {
		t.Fatalf("NodeDelay should error on malformed JSON, got delay=%d", d)
	}
	if d != 0 {
		t.Errorf("delay = %d on error, want 0", d)
	}
}

func TestClashSetSelectorRejectsBadTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // clash: target not in selector
	}))
	defer srv.Close()

	c := NewClashClient(srv.URL, "")
	if err := c.SetSelector(context.Background(), "vpn-pool", "nope"); err == nil {
		t.Error("SetSelector should error on 400")
	}
}
