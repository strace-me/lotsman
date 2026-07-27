// Command lotsman-gui is the desktop window (Wails v2 + Svelte). It is unprivileged
// and links only client/control; it never touches the data plane. State lives in
// the service — this renders /status and issues actions over the socket.
package main

import (
	"embed"
	"flag"
	"log"
	"os"
	"strconv"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// tuneWaylandDisplay works around WebKitGTK's broken Wayland HiDPI path on wlroots
// compositors (Hyprland/Sway). It does two things, and only on a Wayland session:
//
//  1. Backend — under a fractional or 2× output scale the webview computes a NEGATIVE
//     device-pixel-ratio and a multi-billion-pixel viewport, which collapses the whole
//     layout (tiles overlap, everything crams into a corner). This is a GTK3/webkit2gtk
//     limitation (no wp_fractional_scale support), not fixable via env or webkit flags,
//     and unchanged across Wails 2.10→2.12. Routing the GTK webview through XWayland
//     gives a clean integer scale and a correct viewport. Native X11 sessions already
//     take this path and are unaffected; a user whose native Wayland path works can
//     force it back with GDK_BACKEND=wayland.
//
//  2. Scale — XWayland does not receive the Wayland output scale, so on a HiDPI display
//     the window renders too small. scale (from -scale) applies GDK_SCALE to restore it.
//     We never override a GDK_SCALE the session already exports.
//
// Must run before wails.Run, which initialises GTK and reads these once.
func tuneWaylandDisplay(scale int) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	if os.Getenv("GDK_BACKEND") == "" {
		os.Setenv("GDK_BACKEND", "x11")
	}
	if scale > 0 && os.Getenv("GDK_SCALE") == "" {
		os.Setenv("GDK_SCALE", strconv.Itoa(scale))
	}
}

func main() {
	socket := flag.String("socket", "", "control socket path (empty = default under the runtime dir)")
	scale := flag.Int("scale", 0, "HiDPI scale factor for the window (0 = leave to the session; set e.g. 2 on a Wayland/wlroots HiDPI display where the XWayland fallback renders too small)")
	flag.Parse()

	tuneWaylandDisplay(*scale)

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
