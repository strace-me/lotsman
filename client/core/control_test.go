package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/audit"
)

// shortTempDir keeps the socket path under the sockaddr_un limit: the default
// temp dir on macOS is long enough on its own to blow past it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// unixClient dials the control socket the way a tray would.
func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func startControl(t *testing.T, stop func()) (string, *ControlServer) {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "control.sock")
	s := NewControlServer(&Core{}, stop, slog.New(slog.DiscardHandler))
	if err := s.Serve(path); err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return path, s
}

func TestControlStatusIsServedOverTheSocket(t *testing.T) {
	path, _ := startControl(t, func() {})

	resp, err := unixClient(path).Get("http://unix/status")
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}
	var got Status
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A Core that never Started reports not-running rather than erroring, so a UI
	// attaching early gets an answer instead of a broken pipe.
	if got.Running {
		t.Error("a core that has not started must not report running")
	}
}

func TestControlStopIsAcknowledgedBeforeTeardown(t *testing.T) {
	stopped := make(chan struct{})
	path, _ := startControl(t, func() { close(stopped) })

	resp, err := unixClient(path).Post("http://unix/stop", "", nil)
	if err != nil {
		t.Fatalf("post stop: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status code = %d, want 202", resp.StatusCode)
	}
	// The acknowledgement must arrive intact — the caller should not see a dropped
	// connection because teardown raced the response.
	if body, _ := io.ReadAll(resp.Body); len(body) == 0 {
		t.Error("stop must acknowledge before tearing down")
	}
	<-stopped // the callback still fires
}

func TestControlSocketIsPrivateAndReplacesAStaleOne(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "control.sock")
	// A process killed with -9 leaves the socket file behind; binding must not
	// fail because of it.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}

	s := NewControlServer(&Core{}, func() {}, slog.New(slog.DiscardHandler))
	if err := s.Serve(path); err != nil {
		t.Fatalf("serve over a stale socket: %v", err)
	}
	defer s.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %o, want 0600 — a writable socket hands over the tunnel", perm)
	}
}

func TestControlCloseRemovesTheSocket(t *testing.T) {
	path, s := startControl(t, func() {})
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Close must remove the socket so the next start binds cleanly")
	}
}

func TestControlEventsIsServedOverTheSocket(t *testing.T) {
	path, _ := startControl(t, func() {})

	resp, err := unixClient(path).Get("http://unix/events")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}
	// A core that never started has no history, but the endpoint still answers
	// cleanly rather than erroring — a UI attaching early gets an empty list.
	var got []audit.Transition
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestControlRecheckRejectsUnknownService(t *testing.T) {
	path, _ := startControl(t, func() {})

	resp, err := unixClient(path).Post("http://unix/service/nope/recheck", "", nil)
	if err != nil {
		t.Fatalf("post recheck: %v", err)
	}
	defer resp.Body.Close()
	// A never-started core has no probing engine; a recheck must be refused with a
	// 400, not panic on the nil registry.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status code = %d, want 400", resp.StatusCode)
	}
}

// validStarterConfig parses cleanly (builtin `streaming` category supplies the chain).
const validStarterConfig = `subscriptions:
  - { name: s1, url: "https://example.com/sub", format: auto, tags: [normal], enabled: true }
services:
  - name: youtube
    category: streaming
    probe_target: https://www.youtube.com/generate_204
    domains: [youtube.com]
`

func TestControlConfigEditingDisabledByDefault(t *testing.T) {
	path, _ := startControl(t, func() {})
	// Without WithConfig the file endpoints must refuse rather than expose a path.
	resp, err := unixClient(path).Get("http://unix/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET /config without WithConfig = %d, want 501", resp.StatusCode)
	}
}

func TestControlConfigValidate(t *testing.T) {
	path, _ := startControl(t, func() {})
	post := func(yaml string) int {
		body, _ := json.Marshal(map[string]string{"yaml": yaml})
		resp, err := unixClient(path).Post("http://unix/config/validate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post validate: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("this: [is not: valid: lotsman"); code != http.StatusBadRequest {
		t.Errorf("invalid config validate = %d, want 400", code)
	}
	if code := post(validStarterConfig); code != http.StatusOK {
		t.Errorf("valid config validate = %d, want 200", code)
	}
}

func TestControlConfigGetReturnsTheFile(t *testing.T) {
	dir := shortTempDir(t)
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(validStarterConfig), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	path, s := startControl(t, func() {})
	s.WithConfig(cfg, func() {}) // handlers read configPath live, so post-Serve is fine

	resp, err := unixClient(path).Get("http://unix/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /config = %d, want 200", resp.StatusCode)
	}
	var got struct{ Path, YAML string }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Path != cfg || got.YAML != validStarterConfig {
		t.Errorf("config mismatch: path=%q yaml-len=%d", got.Path, len(got.YAML))
	}
}
