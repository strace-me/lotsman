package control

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// serveOnSocket starts an HTTP server on a unix socket and returns its path.
func serveOnSocket(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lc-ctl")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return path
}

func TestStatusDecodesTheRichReport(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{
			"running": true,
			"verdict": {"state":"partial","working":7,"failing":1,"broken":2,"total":10},
			"network": {"id":"abc","kind":"wifi","iface":"wlan0","roaming":false},
			"engines": [{"name":"sing-box","state":"running"},{"name":"nfqws","state":"stopped"}],
			"fleet": {"total":150},
			"subscriptions": [{"name":"demo","usedBytes":8,"totalBytes":10,"fractionUsed":0.8,"daysUntilExpire":12,"expired":false}],
			"services": [{"service":"youtube","state":"VPN","node":"nl-01","rung":0,"rungClass":"vpn","strategy":"vpn_pool","stalledRatio":0,"leakRatio":0,"broken":false}]
		}`))
	})
	c := New(serveOnSocket(t, mux))

	rep, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !rep.Running || rep.Verdict.State != "partial" || rep.Verdict.Failing != 1 {
		t.Errorf("verdict decode wrong: %+v", rep.Verdict)
	}
	if rep.Network.ID != "abc" || rep.Fleet.Total != 150 || len(rep.Engines) != 2 {
		t.Errorf("sections decode wrong: %+v", rep)
	}
	if len(rep.Services) != 1 || rep.Services[0].RungClass != "vpn" {
		t.Errorf("services decode wrong: %+v", rep.Services)
	}
	if len(rep.Subscriptions) != 1 || rep.Subscriptions[0].FractionUsed != 0.8 {
		t.Errorf("subs decode wrong: %+v", rep.Subscriptions)
	}
}

func TestEventsPassesFiltersAndDecodes(t *testing.T) {
	var gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[{"time":"2026-07-26T14:28:00Z","service":"ai","from_position":0,"to_position":3,"state":"BROKEN","reason":"chain exhausted"}]`))
	})
	c := New(serveOnSocket(t, mux))

	ev, err := c.Events(context.Background(), 5, "ai")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if gotQuery != "limit=5&service=ai" {
		t.Errorf("query = %q, want limit=5&service=ai", gotQuery)
	}
	if len(ev) != 1 || ev[0].Service != "ai" || ev[0].ToPosition != 3 {
		t.Errorf("event decode wrong: %+v", ev)
	}
}

func TestRecheckPostsToTheServicePath(t *testing.T) {
	var gotPath, gotMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /service/{name}/recheck", func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusAccepted)
	})
	c := New(serveOnSocket(t, mux))

	if err := c.Recheck(context.Background(), "youtube"); err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/service/youtube/recheck" {
		t.Errorf("recheck hit %s %s", gotMethod, gotPath)
	}
}

func TestRecheckSurfacesA400AsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /service/{name}/recheck", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unknown service \"nope\"", http.StatusBadRequest)
	})
	c := New(serveOnSocket(t, mux))

	if err := c.Recheck(context.Background(), "nope"); err == nil {
		t.Error("a 400 must surface as an error")
	}
}
