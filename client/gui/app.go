package main

import (
	"context"
	"encoding/json"

	"github.com/strace-me/lotsman/client/control"
	"github.com/strace-me/lotsman/pkg/version"
)

// App is the Wails backend. It is a deliberately thin pass-through to the control
// socket: every method just forwards to client/control, so all state and policy
// stay in the service and the GUI only renders + issues actions. Wails binds these
// exported methods to the frontend (window.go.main.App.*).
type App struct {
	ctx    context.Context
	client *control.Client
}

// NewApp builds the backend against socket (empty = the default runtime path).
func NewApp(socket string) *App {
	if socket == "" {
		socket = control.DefaultSocketPath()
	}
	return &App{client: control.New(socket)}
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// OwnVersion is THIS WINDOW's build identity, stamped by scripts/build.sh into
// pkg/version like every other target.
//
// The bar shows the SERVICE's version, because what a reader needs first is which
// build is answering. But the GUI is the one target build.sh does not build — it
// needs wails and a webkit toolchain — so it is built by hand, separately, and it
// drifts. On 2026-08-07 the window on the owner's laptop was two commits older
// than its service, and the only thing that gave it away was a hint sentence he
// happened to screenshot. A drift nobody can see is a drift nobody fixes.
func (a *App) OwnVersion() string { return version.String() }

// Status returns the rich /status the whole UI renders from.
func (a *App) Status() (control.Report, error) { return a.client.Status(a.ctx) }

// Events returns recent brain transitions newest-first (the "история событий").
func (a *App) Events(limit int, service string) ([]control.Event, error) {
	return a.client.Events(a.ctx, limit, service)
}

// Recheck forces an immediate probe of one service ("перепроверить").
func (a *App) Recheck(service string) error { return a.client.Recheck(a.ctx, service) }

// SetServiceEnabled switches one service on or off (the tile toggle). The daemon
// writes the bit to the config file, so it survives a restart and is not undone by
// the next reassert.
func (a *App) SetServiceEnabled(service string, enabled bool) error {
	return a.client.SetServiceEnabled(a.ctx, service, enabled)
}

// Stop is the master OFF: it tears the service down.
func (a *App) Stop() error { return a.client.Stop(a.ctx) }

// Config returns the service's current config file (raw YAML + its path).
func (a *App) Config() (control.ConfigDoc, error) { return a.client.Config(a.ctx) }

// ValidateConfig checks a candidate raw-YAML config without writing it.
func (a *App) ValidateConfig(yaml string) error { return a.client.ValidateConfig(a.ctx, yaml) }

// SetConfig validates, writes and applies a raw-YAML config — the escape hatch.
func (a *App) SetConfig(yaml string) error { return a.client.SetConfig(a.ctx, yaml) }

// ValidateConfigDoc checks a structured config document without writing it.
func (a *App) ValidateConfigDoc(doc map[string]any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return a.client.ValidateConfigDoc(a.ctx, b)
}

// SaveConfig validates, writes and applies a STRUCTURED config document — the
// primary path. The daemon re-serialises the structure to YAML and owns the file.
func (a *App) SaveConfig(doc map[string]any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return a.client.SetConfigDoc(a.ctx, b)
}
