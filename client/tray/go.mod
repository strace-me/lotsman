// The tray is a SEPARATE, unprivileged process (its own module so its cgo/systray
// deps never touch the main build). It owns nothing: it reads the service's rich
// /status over the control socket, recolours a glyph by verdict, and launches the
// GUI window on click. See README.md — built on the target (Linux), not the Mac.
module github.com/strace-me/lotsman/client/tray

go 1.26.3

require (
	fyne.io/systray v1.11.0
	github.com/strace-me/lotsman v0.0.0
)

replace github.com/strace-me/lotsman => ../..
