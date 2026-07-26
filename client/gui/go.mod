// The GUI window is a SEPARATE, unprivileged Wails v2 app (its own module so the
// webview/cgo deps never touch the main build). Its Go side is a thin pass-through
// to the control socket via client/control; all state lives in the service. Built
// on the target with `wails build` — see README.md. Run `go mod tidy` first to
// resolve Wails' indirect deps.
module github.com/strace-me/lotsman/client/gui

go 1.26.3

require (
	github.com/strace-me/lotsman v0.0.0
	github.com/wailsapp/wails/v2 v2.10.1
)

replace github.com/strace-me/lotsman => ../..
