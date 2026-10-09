# lotsman-tray — systray companion (separate process)

Unprivileged, its own module. It owns nothing: it reads the service's `/status`
over the control socket, recolours a sextant glyph by verdict, and offers Open
(launch the GUI), Stop (master off), and Quit-the-tray. The privilege boundary is
the socket — this process links only `client/control`, never the engine.

`icon_{neutral,amber,red}.png` are rendered from `../desktop/assets/tray.svg` in
three state colours and embedded (systray takes raster bytes, so state colour can't
be a runtime `currentColor`).

## Build (on the target — Linux/NixOS)

`fyne.io/systray` needs the tray backend libs.

NixOS dev shell (example):

```sh
nix-shell -p go pkg-config libayatana-appindicator gtk3
```

Then, from this directory:

```sh
go mod tidy
go build -o lotsman-tray .
./lotsman-tray -gui ./lotsman-gui     # -socket <path> to override the default
```

## TODO

- Proper per-panel theming: a macOS template icon and SNI theme-following on Linux;
  the embedded state colours are a first pass.
- A richer menu: the per-rule recheck and enable/disable endpoints both EXIST now
  (`POST /service/{name}/recheck`, `POST /service/{name}/enabled`) — only the tray
  does not call them yet. The enable/disable one edits that rule's own file and
  preserves its comments.
