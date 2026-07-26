// Command lotsman-gui is the desktop window (Wails v2 + Svelte). It is unprivileged
// and links only client/control; it never touches the data plane. State lives in
// the service — this renders /status and issues actions over the socket.
package main

import (
	"embed"
	"flag"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
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
