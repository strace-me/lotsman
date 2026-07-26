package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
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
	core *Core
	stop func()
	srv  *http.Server
	ln   net.Listener
	log  *slog.Logger
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
	// Belt and braces: the directory is already 0700, but a socket another user
	// could write to would hand them the tunnel.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("core: control socket perms: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /stop", s.handleStop)
	mux.HandleFunc("POST /service/{name}/recheck", s.handleRecheck)
	mux.HandleFunc("GET /events", s.handleEvents)

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
