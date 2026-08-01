// Package mobile is the Android half of the Lotsman client. It reuses
// client/core UNCHANGED and swaps only the platform seam: instead of spawning an
// external `sing-box` binary (client/platform/externalbox) it drives sing-box
// in-process through libbox, which obtains the tun fd by calling back into Kotlin
// (PlatformInterface.OpenTun) rather than creating a tun itself.
//
// Everything here runs inside ONE Android process — the VpnService. There is no
// unix control socket: Kotlin calls the facade in facade.go directly over the
// gomobile bindings.
//
// This package MUST be built with -tags with_clash_api,with_quic,with_utls,with_gvisor.
// The untagged build compiles and is useless; see the header of go.mod.
package mobile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/strace-me/lotsman/client/core"
)

// AndroidProxyCore implements core.ProxyCore on top of libbox.
//
// Two structural differences from externalbox drive most of what follows:
//
//   - There is no child process, so there is no "is the process alive" signal to
//     read. libbox 1.13 keeps its service status private and publishes it only
//     over the gRPC stream we deliberately never start, so Alive reports the last
//     state libbox reported TO US. See Alive for exactly what that does and does
//     not measure.
//   - libbox is fed a config STRING, not a path. The reconciler in
//     pkg/reconcile still owns a file (it backs it up and may roll back to it),
//     and core.ProxyCore.Restart is defined as "re-read the config already on
//     disk" — so this type keeps writing that file and Restart reads it back.
type AndroidProxyCore struct {
	// configPath must be the SAME path handed to core.Options.SingboxConfig: the
	// reconciler writes there and then calls Restart expecting us to read it.
	configPath string
	log        *slog.Logger

	mu     sync.Mutex
	server *libbox.CommandServer
	// override carries the per-app routing decision. libbox REJECTS include_uid /
	// exclude_uid inside the tun JSON (platformInterfaceWrapper.OpenInterface errors
	// on them), so package lists are passed here at StartOrReloadService time and
	// libbox appends them to the tun inbound itself.
	//
	// It must never be nil: CommandServer.StartOrReloadService dereferences it
	// unconditionally.
	override *libbox.OverrideOptions
	// startedOK is set only when libbox accepted a start, and cleared on stop or on
	// a start that failed. It is a record of what we were told, not a measurement.
	startedOK bool
}

var _ core.ProxyCore = (*AndroidProxyCore)(nil)

// NewAndroidProxyCore builds the libbox-backed ProxyCore.
//
// libbox.Setup MUST have been called first (it sets the package-global base /
// working / temp paths that libbox.NewCommandServer reads while building its base
// context), and platform must be the Kotlin-side PlatformInterface — libbox has no
// other way to get a tun fd on Android.
//
// The returned CommandServer's gRPC listener is deliberately NOT started: only
// CommandServer.Start() binds it, and we never call that. StartOrReloadService and
// CloseService work without it, so the whole command-socket surface stays absent
// rather than merely secured.
func NewAndroidProxyCore(configPath string, platform libbox.PlatformInterface, log *slog.Logger) (*AndroidProxyCore, error) {
	if platform == nil {
		return nil, errors.New("mobile: no PlatformInterface — libbox cannot obtain a tun fd without the Kotlin side")
	}
	server, err := libbox.NewCommandServer(noopCommandHandler{log: log}, platform)
	if err != nil {
		return nil, fmt.Errorf("mobile: libbox command server: %w", err)
	}
	return &AndroidProxyCore{
		configPath: configPath,
		log:        log,
		server:     server,
		override:   &libbox.OverrideOptions{},
	}, nil
}

// SetPackageRouting sets the per-app routing applied on the next start/reload.
// Nil iterators mean "route every app", which is what v1 ships. It takes
// libbox.StringIterator rather than []string because []string is not a type
// gomobile can bind, and libbox already exports this iterator.
func (b *AndroidProxyCore) SetPackageRouting(include, exclude libbox.StringIterator) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.override = &libbox.OverrideOptions{
		// AutoRedirect installs nftables rules and needs root. Unrooted Android has
		// none, so it stays off — same reason v1 ships without the desync rung.
		AutoRedirect:   false,
		IncludePackage: include,
		ExcludePackage: exclude,
	}
}

// Check validates configJSON without applying it. libbox.CheckConfig builds the
// box from the config and closes it again, so this is a real validation — the
// in-process equivalent of `sing-box check -c`.
func (b *AndroidProxyCore) Check(_ context.Context, configJSON []byte) error {
	if err := libbox.CheckConfig(string(configJSON)); err != nil {
		return fmt.Errorf("mobile: libbox check: %w", err)
	}
	return nil
}

// Start brings sing-box up with configJSON. Idempotent, matching externalbox: a
// second Start while running is a no-op rather than a restart.
func (b *AndroidProxyCore) Start(_ context.Context, configJSON []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startedOK {
		return nil
	}
	return b.startLocked(configJSON)
}

// Reload swaps in a new config. libbox's StartOrReloadService closes the old
// instance and builds a new one, so this is a restart with the tun fd re-opened
// through Kotlin — there is no connection-preserving reload, exactly as on desktop.
func (b *AndroidProxyCore) Reload(_ context.Context, configJSON []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startLocked(configJSON)
}

// Restart re-reads the config already on disk WITHOUT rewriting it. The
// reconciler placed that file there and holds the backup it may need to roll back
// to, so re-serialising our in-memory copy over it would defeat the rollback.
func (b *AndroidProxyCore) Restart(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cfg, err := os.ReadFile(b.configPath)
	if err != nil {
		return fmt.Errorf("mobile: restart: read %s: %w", b.configPath, err)
	}
	return b.startOrReloadLocked(cfg)
}

// Stop tears the instance down. Idempotent: libbox answers os.ErrInvalid when the
// service is not in a stoppable state, which for our purposes means "already
// stopped" and is not an error to propagate.
func (b *AndroidProxyCore) Stop(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startedOK = false
	if err := b.server.CloseService(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("mobile: libbox close service: %w", err)
	}
	return nil
}

// Close releases the CommandServer itself (its observables). Not part of
// core.ProxyCore; the facade calls it after Stop when the VpnService goes away.
func (b *AndroidProxyCore) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startedOK = false
	b.server.Close()
}

// Alive reports whether sing-box is currently up — WITH A CAVEAT THAT MATTERS.
//
// It is NOT a measurement. libbox 1.13 holds its service status in an unexported
// field of daemon.StartedService and publishes it only through the gRPC
// SubscribeServiceStatus stream, which this build never starts. So what is
// returned here is the conjunction of two weak signals: libbox accepted our last
// start, and it still holds an Instance. A box that died on its own afterwards
// (an internal fatal, the OOM killer service) leaves both of those true, so this
// would still say "alive".
//
// This is survivable only because core never trusts Alive alone: superviseBox
// pairs it with controlAlive, which really does dial the loopback Clash-API, and
// the reconciler uses controlAlive for its rollback decision. Do not build
// anything on Alive that is not similarly backed.
func (b *AndroidProxyCore) Alive(_ context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startedOK && b.server.Instance() != nil
}

// startLocked persists the config the way externalbox does — the reconciler and
// Restart both expect the live config to exist at configPath — and then hands it
// to libbox. Caller holds b.mu.
func (b *AndroidProxyCore) startLocked(configJSON []byte) error {
	if b.configPath != "" {
		if err := os.WriteFile(b.configPath, configJSON, 0o600); err != nil {
			return fmt.Errorf("mobile: write config: %w", err)
		}
	}
	return b.startOrReloadLocked(configJSON)
}

// startOrReloadLocked is the single place that talks to libbox's lifecycle, so
// startedOK can never drift from what libbox last told us. Caller holds b.mu.
func (b *AndroidProxyCore) startOrReloadLocked(configJSON []byte) error {
	b.startedOK = false
	if err := b.server.StartOrReloadService(string(configJSON), b.override); err != nil {
		return fmt.Errorf("mobile: libbox start: %w", err)
	}
	b.startedOK = true
	return nil
}

// noopCommandHandler satisfies libbox.CommandServerHandler. Every method on it is
// reached only through the gRPC command server, which this build never starts, so
// none of them should ever fire; they are implemented as honest no-ops rather than
// as panics, since a panic here would take the whole app down.
//
// SystemProxy is a desktop concept (an OS-level HTTP proxy setting). Android has
// no such thing for a VpnService, so Available is false — reported, not faked.
type noopCommandHandler struct{ log *slog.Logger }

func (h noopCommandHandler) ServiceStop() error {
	h.log.Warn("libbox: ServiceStop over the command server, which is not started — ignoring")
	return nil
}

func (h noopCommandHandler) ServiceReload() error {
	h.log.Warn("libbox: ServiceReload over the command server, which is not started — ignoring")
	return nil
}

func (h noopCommandHandler) GetSystemProxyStatus() (*libbox.SystemProxyStatus, error) {
	return &libbox.SystemProxyStatus{Available: false, Enabled: false}, nil
}

func (h noopCommandHandler) SetSystemProxyEnabled(bool) error {
	return errors.New("mobile: no system proxy on Android")
}

func (h noopCommandHandler) WriteDebugMessage(message string) {
	h.log.Debug("libbox", "msg", message)
}
