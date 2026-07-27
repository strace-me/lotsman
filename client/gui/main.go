// Command lotsman-gui is the desktop window (Wails v2 + Svelte). It is unprivileged
// and links only client/control; it never touches the data plane. State lives in
// the service — this renders /status and issues actions over the socket.
package main

import (
	"embed"
	"flag"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// preferXWaylandOnWayland works around a WebKitGTK HiDPI bug on wlroots compositors
// (Hyprland/Sway): under a fractional or 2× output scale the webview computes a
// NEGATIVE device-pixel-ratio and a multi-billion-pixel viewport, which collapses the
// whole layout (tiles overlap, everything crams into a corner). Routing the GTK webview
// through XWayland gives a clean integer scale and a correct viewport. Native X11
// sessions already run this path and are unaffected; a user on a Wayland compositor
// where the native path works can force it back with GDK_BACKEND=wayland.
//
// Must run before wails.Run, which initialises GTK and reads GDK_BACKEND once.
func preferXWaylandOnWayland() {
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("GDK_BACKEND") == "" {
		os.Setenv("GDK_BACKEND", "x11")
	}
}

func main() {
	preferXWaylandOnWayland()

	socket := flag.String("socket", "", "control socket path (empty = default under the runtime dir)")
	flag.Parse()

	app := NewApp(*socket)
	err := wails.Run(&options.App{
		Title:       "Lotsman",
		Width:       980,
		Height:      680,
		MinWidth:    720,
		MinHeight:   520,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   app.startup,
		Bind:        []any{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
