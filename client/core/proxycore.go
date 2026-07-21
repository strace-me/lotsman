// Package core composes the Lotsman control-plane spine (brain, kb, applier,
// probing, dataplane) into a single-device client. It reuses those packages
// unchanged and drives sing-box through one platform seam — ProxyCore for the
// box lifecycle — plus the loopback Clash-API for node/direct steering, which
// every sing-box backend (external binary or in-process libbox) exposes the same.
package core

import "context"

// ProxyCore owns the sing-box instance lifecycle for one platform. Desktop
// (externalbox) spawns an external `sing-box run`; Android (later) drives an
// in-process libbox service. The Core generates the config — including the
// mandatory Clash-API Bearer secret — and hands it here; node/direct steering
// happens separately over the loopback Clash-API, so this seam is lifecycle only.
type ProxyCore interface {
	// Check validates configJSON without applying it.
	Check(ctx context.Context, configJSON []byte) error
	// Start brings sing-box up with configJSON. Idempotent.
	Start(ctx context.Context, configJSON []byte) error
	// Reload swaps in a new config (a structure change), restarting as needed.
	Reload(ctx context.Context, configJSON []byte) error
	// Stop tears the instance down.
	Stop(ctx context.Context) error
	// Alive reports whether sing-box is currently up.
	Alive(ctx context.Context) bool
}
