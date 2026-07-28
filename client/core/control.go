package core

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/strace-me/lotsman/pkg/config"
)

// maxUnixSocketPath is a conservative bound below the smallest sockaddr_un limit
// across the platforms this client targets.
const maxUnixSocketPath = 100

// ControlServer is the seam an unprivileged UI attaches to. The service owns the
// tunnel, the desync and the brain; a tray or CLI only asks what is happening and
// tells it to stop.
//
// It listens on a UNIX SOCKET rather than a loopback port on purpose. Loopback is
// not private on a shared machine — any co-resident process can reach
// 127.0.0.1 — whereas a socket in a directory we own is gated by filesystem
// permissions, which the kernel enforces for us. That is the same reasoning that
// made the Clash-API secret mandatory, applied one layer up.
type ControlServer struct {
	core        *Core
	stop        func()
	restart     func() // re-exec the service to apply a new config (nil = config editing off)
	configPath  string // path of the config file the GUI edits ("" = config editing off)
	socketGroup string // group to own the control socket 0660 ("" = owner-only 0600)
	srv         *http.Server
	ln          net.Listener
	log         *slog.Logger
}

// Status is the ORIGINAL /status shape: whether the data plane is actually up
// (sing-box alive and answering — not merely configured) and where each service
// sits. GET /status now serves the richer Report (see status.go), which keeps
// these two fields at the same JSON paths — so this remains a valid decode target
// for a minimal client that wants only running+services.
type Status struct {
	Running  bool         `json:"running"`
	Services []NodeStatus `json:"services"`
}

// NewControlServer returns a server for core. stop is invoked by POST /stop.
func NewControlServer(core *Core, stop func(), log *slog.Logger) *ControlServer {
	return &ControlServer{core: core, stop: stop, log: log}
}

// WithConfig enables the config-editing endpoints (GET /config, POST /config,
// POST /config/validate). The server reads and writes the config file at path and
// applies a saved config by calling restart (a graceful Stop + re-exec). Left off
// by default so a caller that does not want in-app config editing — or a test —
// simply never enables it.
func (s *ControlServer) WithConfig(path string, restart func()) *ControlServer {
	s.configPath = path
	s.restart = restart
	return s
}

// WithSocketGroup opens the control socket to a UNIX group so an unprivileged UI
// running as a member of it can attach to a root service without itself being
// root. The socket becomes group-owned 0660 (and the directory reaching it
// group-traversable 0750) instead of the owner-only 0600 default. Left empty the
// socket stays private to the owning user — the safe default that suits a
// same-user run. This is the mechanism the systemd unit's Group= relies on; the
// group must already exist. Named a group that does not resolve, Serve fails
// loudly rather than silently falling back to a wider or narrower mode.
func (s *ControlServer) WithSocketGroup(group string) *ControlServer {
	s.socketGroup = group
	return s
}

// Serve binds path and serves in the background until Close. A stale socket left
// by a killed process is removed first — otherwise bind fails and the service
// refuses to start for no reason the operator can see.
func (s *ControlServer) Serve(path string) error {
	// sockaddr_un bounds the path at 104 bytes on darwin and 108 on Linux, and
	// overflowing it fails with a bare "invalid argument" that says nothing about
	// the length. Name the real problem instead.
	if len(path) > maxUnixSocketPath {
		return fmt.Errorf("core: control socket path is %d bytes (%q); the kernel limit is about %d — use a shorter path",
			len(path), path, maxUnixSocketPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("core: control socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("core: stale control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("core: control socket %s: %w", path, err)
	}
	if err := s.lockSocket(path); err != nil {
		ln.Close()
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /stop", s.handleStop)
	mux.HandleFunc("POST /service/{name}/recheck", s.handleRecheck)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /config", s.handleGetConfig)
	mux.HandleFunc("POST /config", s.handleSetConfig)
	mux.HandleFunc("POST /config/validate", s.handleValidateConfig)

	s.ln = ln
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Warn("control server", "err", err)
		}
	}()
	s.log.Info("control socket listening", "path", path)
	return nil
}

// lockSocket sets who may drive the tunnel through the control socket. The socket
// is the privilege seam — a process that can write to it can stop the tunnel or
// rewrite the config — so it is locked to the owning user (0600) by default. When
// a group is configured the socket becomes group-owned 0660 and the directory
// reaching it group-traversable 0750, so an unprivileged UI in that group can
// attach to a root service; the directory perms matter because a socket inside an
// unreadable directory is unreachable however its own mode reads. Chowning to a
// group the caller already belongs to is permitted unprivileged, so this works
// both under the root systemd service and in a same-user dev run.
func (s *ControlServer) lockSocket(path string) error {
	if s.socketGroup == "" {
		// Belt and braces: the directory is already 0700, but a socket another user
		// could write to would hand them the tunnel.
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("core: control socket perms: %w", err)
		}
		return nil
	}
	g, err := user.LookupGroup(s.socketGroup)
	if err != nil {
		return fmt.Errorf("core: control socket group %q: %w", s.socketGroup, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return fmt.Errorf("core: control socket group %q has non-numeric gid %q: %w", s.socketGroup, g.Gid, err)
	}
	dir := filepath.Dir(path)
	if err := os.Chown(dir, -1, gid); err != nil {
		return fmt.Errorf("core: control socket dir group: %w", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return fmt.Errorf("core: control socket dir perms: %w", err)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		return fmt.Errorf("core: control socket group: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("core: control socket perms: %w", err)
	}
	return nil
}

// Close stops serving and removes the socket.
func (s *ControlServer) Close() error {
	if s.srv == nil {
		return nil
	}
	err := s.srv.Close()
	if s.ln != nil {
		if unixAddr, ok := s.ln.Addr().(*net.UnixAddr); ok {
			os.Remove(unixAddr.Name)
		}
	}
	return err
}

func (s *ControlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	// The rich Report is a backward-compatible superset of the old {running,
	// services}: Running still reflects real data-plane liveness (LOT-49), and the
	// legacy fields keep their JSON paths, so an old tray decoding Status is
	// unaffected while a new UI reads the fleet/network/engines/subscription sections.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.core.Report(r.Context()))
}

func (s *ControlServer) handleStop(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"stopping":true}`))
	// Return the response before tearing the service down, so the caller sees the
	// acknowledgement rather than a dropped connection.
	go s.stop()
}

// handleRecheck forces an immediate probe of one service — the UI's "recheck now".
// 400 on an unknown service so a stray request cannot wedge the engine.
func (s *ControlServer) handleRecheck(w http.ResponseWriter, r *http.Request) {
	if err := s.core.Recheck(r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"rechecking":true}`))
}

// handleEvents returns recent brain rung-transitions newest-first (the "история
// событий" surface). ?service= filters to one service; ?limit=N caps the count.
func (s *ControlServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.core.Events(limit, r.URL.Query().Get("service")))
}

// configDoc carries the config as the GUI works with it: the raw on-disk YAML plus
// its path, AND — the primary path — the structured Document. A save (or validate)
// carrying Doc uses it (the service turns structure → YAML → validation → file), so
// the UI never handles YAML itself; one carrying only YAML is the raw escape hatch.
type configDoc struct {
	Path string           `json:"path"`
	YAML string           `json:"yaml,omitempty"`
	Doc  *config.Document `json:"doc,omitempty"`
}

// yamlBytes resolves the YAML to validate/write: the structured Doc when present
// (re-serialised by the service), else the raw YAML text.
func (in configDoc) yamlBytes() ([]byte, error) {
	if in.Doc != nil {
		return in.Doc.YAML()
	}
	return []byte(in.YAML), nil
}

func (s *ControlServer) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	if s.configPath == "" {
		http.Error(w, "in-app config editing is not enabled on this instance", http.StatusNotImplemented)
		return
	}
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		http.Error(w, "read config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := configDoc{Path: s.configPath, YAML: string(data)}
	// The structured view is best-effort: if the file does not even parse
	// structurally, the UI still gets the raw YAML to repair by hand.
	if doc, derr := config.ParseDocument(data); derr == nil {
		out.Doc = doc
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleValidateConfig parses a candidate config WITHOUT writing it, so the editor
// can offer a "check" that never risks the running config.
func (s *ControlServer) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	in, err := readConfigDoc(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	y, err := in.yamlBytes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := config.Parse(y); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"valid":true}`))
}

// handleSetConfig validates a candidate config, writes it atomically, and applies
// it by re-executing the service. It validates BEFORE touching the file — a config
// that fails to parse is never written, so the service can always restart into a
// good file. That is the whole safety of editing config from a UI.
func (s *ControlServer) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" || s.restart == nil {
		http.Error(w, "in-app config editing is not enabled on this instance", http.StatusNotImplemented)
		return
	}
	in, err := readConfigDoc(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	y, err := in.yamlBytes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := config.Parse(y)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := writeFileAtomic(s.configPath, y, 0o600); err != nil {
		http.Error(w, "write config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"applied":true}`))
	// Apply AFTER the ack, so the caller sees success before the data plane blips.
	// Prefer an in-place reload — it restarts sing-box only when its config actually
	// changed, so a non-routing edit keeps every live connection; fall back to a full
	// re-exec if the reload cannot apply in place.
	go func() {
		if err := s.core.Reload(cfg); err != nil {
			s.log.Warn("in-place config reload failed — falling back to re-exec", "err", err)
			s.restart()
		}
	}()
}

func readConfigDoc(r *http.Request) (configDoc, error) {
	var in configDoc
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		return configDoc{}, fmt.Errorf("bad request: %w", err)
	}
	return in, nil
}

// writeFileAtomic writes via a temp file + rename, so a crash mid-write cannot
// leave a half-written (unparseable) config that the next start would choke on.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
