package main

import (
	"context"

	"github.com/strace-me/lotsman/client/control"
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

// Status returns the rich /status the whole UI renders from.
func (a *App) Status() (control.Report, error) { return a.client.Status(a.ctx) }

// Events returns recent brain transitions newest-first (the "история событий").
func (a *App) Events(limit int, service string) ([]control.Event, error) {
	return a.client.Events(a.ctx, limit, service)
}

// Recheck forces an immediate probe of one service ("перепроверить").
func (a *App) Recheck(service string) error { return a.client.Recheck(a.ctx, service) }

// Stop is the master OFF: it tears the service down.
func (a *App) Stop() error { return a.client.Stop(a.ctx) }
