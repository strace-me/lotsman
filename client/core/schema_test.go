package core

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The window and tray read nothing from disk — they talk to this socket. So a
// metadata document that is not served here is a document the UI cannot reach, and
// "one source of truth for the enums" quietly stops being true.
func TestSchemaIsServedOverTheControlSocket(t *testing.T) {
	server, core := newTestServer(t)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest("GET", "/schema", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// The pieces a UI cannot guess: the enums, the chain actions, and the paths.
	for _, key := range []string{"proxy_engines", "proxy_modes", "desync_engines", "probe_types", "chain_tiers", "chain_actions", "paths", "strategies"} {
		if _, ok := got[key]; !ok {
			t.Errorf("schema is missing %q: %s", key, rec.Body)
		}
	}
	paths, _ := got["paths"].(map[string]any)
	if paths["configuration"] != "/etc/lotsman/configuration.yaml" {
		t.Errorf("paths.configuration = %v", paths["configuration"])
	}
	_ = core
}

func TestSchemaCarriesTheEngineOnEveryStrategy(t *testing.T) {
	// nfqws1 and nfqws2 share a catalogue entry name space. A picker that ignores
	// the engine offers recipes the box cannot run, and a recipe handed to the wrong
	// engine does not fail — it desyncs nothing.
	server, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest("GET", "/schema", nil))

	var doc struct {
		Strategies []struct {
			Instance string `json:"instance"`
			Engine   string `json:"engine"`
			Strategy string `json:"strategy"`
		} `json:"strategies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Strategies) == 0 {
		t.Fatal("no strategies in the document")
	}
	engines := map[string]bool{}
	for _, s := range doc.Strategies {
		if s.Engine == "" {
			t.Fatalf("strategy %q has no engine", s.Strategy)
		}
		engines[s.Engine] = true
	}
	// The catalogue ships both engines, so the document must distinguish them.
	if !engines["nfqws1"] || !engines["nfqws2"] {
		t.Errorf("both engines should appear, got %v", engines)
	}
}

func TestSchemaWorksWithNoConfigLoaded(t *testing.T) {
	// A box still on the v1 config must still be able to render an editor: the
	// static parts are complete without a v2 file.
	server, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest("GET", "/schema", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "chain_actions") {
		t.Error("the static document is incomplete without a config")
	}
}

// A Core built literally, with no box: the schema handler reads only the config
// and the engine name, and New() would insist on a usable data plane for a test
// that never starts one.
func newTestServer(t *testing.T) (*http.ServeMux, *Core) {
	t.Helper()
	core := &Core{opts: Options{DesyncEngine: "nfqws1"}}
	s := NewControlServer(core, func() {}, slog.New(slog.DiscardHandler))
	return s.routes(), core
}
