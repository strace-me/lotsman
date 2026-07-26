# lotsman-gui — desktop window (Wails v2 + Svelte)

Unprivileged. Links only `client/control`; it talks to the service over the unix
control socket (default `$XDG_RUNTIME_DIR/lotsman/control.sock`, or `-socket <path>`).
All state lives in the service — this window renders `/status` and issues actions.

Its own Go module so the webview/cgo deps never touch the main build.

## Build (on the target — Linux/NixOS, not the Mac)

Needs: Go, Node, the [Wails CLI](https://wails.io), and `webkit2gtk` + `gtk3` +
`pkg-config`.

NixOS dev shell (example):

```sh
nix-shell -p go nodejs wails pkg-config webkitgtk_4_1 gtk3
```

Then, from this directory:

```sh
go mod tidy       # resolve Wails' indirect deps (no go.sum committed yet)
wails dev         # hot-reload dev window against a running service
wails build       # → build/bin/lotsman-gui
```

## Shape

- `app.go` — the Wails backend: a thin pass-through (`Status` / `Events` /
  `Recheck` / `Stop`) forwarding to `client/control`. Wails binds these to
  `window.go.main.App.*`.
- `frontend/src/App.svelte` — the dashboard: polls `Status()` + `Events()` every
  2s, renders the verdict banner, engine strip, fleet/network stats, exception-first
  service tiles with `перепроверить`, история событий, and the subscription timer.
  Falls back to mock data in a plain browser (`npm run dev`), so the design renders
  without a running service.

Next: wire the tile on/off toggle and the switch/pin dropdown once the backend
`enable/disable` + pin endpoints land; flesh out the Nodes / Subscriptions / Advanced tabs.
