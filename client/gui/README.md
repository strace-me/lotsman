# lotsman-gui — desktop window (Wails v2 + Svelte)

Unprivileged. Links only `client/control`; it talks to the service over the unix
control socket (default under the runtime dir, or `-socket <path>`).
All state lives in the service — this window renders `/status`, issues actions, and
edits the config **through the socket**, never touching a file itself.

Its own Go module so the webview/cgo deps never touch the main build.

## Build (on the target — Linux/NixOS, not the Mac)

Needs: Go, Node, the [Wails CLI](https://wails.io), and `webkit2gtk` + `gtk3` +
`pkg-config`.

NixOS dev shell (example):

```sh
nix-shell shell.nix
```

Then, from this directory:

```sh
go mod tidy                       # resolve Wails' indirect deps (no go.sum committed yet)
wails dev -tags webkit2_41        # hot-reload dev window against a running service
wails build -tags webkit2_41      # → build/bin/lotsman-gui
```

**The `webkit2_41` tag is not optional.** Wails v2 looks for `webkit2gtk-4.0` by
default and nixpkgs has **removed** it (`webkitgtk_4_0` is a stub that errors with
"has been removed, port to `libsoup_3`"). Without the tag the build dies at pkg-config
with `No package 'webkit2gtk-4.0' found` — which is exactly what the documented shell
here used to produce. `scripts/build.sh` already passed the tag; this file did not, and
neither did the README.

Nothing else about the toolchain needs `-tags`: `wails build` fails only on the
pkg-config lookup for webkit.

## Shape

- `app.go` — the Wails backend, a thin pass-through to `client/control`. Bound to
  `window.go.main.App.*`:

  | Method | Route |
  |---|---|
  | `Status` / `Events` | `GET /status`, `GET /events` |
  | `Recheck` | `POST /service/{name}/recheck` |
  | `SetServiceEnabled` | `POST /service/{name}/enabled` |
  | `RefreshSubscriptions` | `POST /subscriptions/refresh` |
  | `Stop` | `POST /stop` |
  | `Config` | `GET /config` |
  | `ValidateConfigDoc` / `SaveConfig` | `POST /config/validate`, `POST /config` |
  | `ValidateConfig` / `SetConfig` | same, document-only (carries live rules) |

- `frontend/src/App.svelte` — the dashboard: polls `Status()` + `Events()` every
  2s, renders the verdict banner, engine strip, fleet/network stats, exception-first
  tiles with `перепроверить`, история событий, and the subscription timer.
  Falls back to mock data in a plain browser (`npm run dev`), so the design renders
  without a running service.

## Config editing — a deployment, not a file

`GET /config` returns `{path, rules_dir, yaml, config, rules[]}`: the document plus
**one entry per rule file**. The editors are per-section and per-file, not one
textarea over a whole YAML — toggling a rule rewrites that rule's own file and
preserves its comments.

`rules` is `json.RawMessage` in the Go client on purpose: **absent** means "leave the
rules alone" and an explicit `[]` means "delete them all". A plain slice cannot tell
those apart after a round-trip, and getting it wrong silently wipes every rule.

Both save paths validate first, and validation runs the **same loader the service
starts with** — a config the editor accepts but the service rejects is worse than no
validation.

## First run

The window asks `SetupState()` **before** `/status`. On a machine that has never been
set up there is no service, so polling first would render "сервис недоступен" — a fault
— over the normal starting state. A machine with a configuration and a dead service is
**broken**, not fresh, and must not be shown first-run: that would invite overwriting a
working config with a new one.

| Method | Route |
|---|---|
| `SetupState` | none — reads the filesystem, works with no service |
| `WriteFirstDeployment(choice, urls)` | none — writes the user deployment directly |

It writes to `$XDG_CONFIG_HOME/lotsman/` (falling back to `~/.config/lotsman`),
unprivileged, validated through the same loader the service starts with, and it
**refuses to overwrite** — the one operation in the window that can destroy work.

`Setup.svelte` renders it and owns the whole window while it is showing. In `vite dev`
it is reachable with `?fresh=1`, because "what does a new user see" is the screen nobody
looks at until it ships and then everybody does.

The service reads a **materialized copy** under `/etc/lotsman`, not this directory — see
`pkg/config/materialize.go` and CODEMAP. One reader, one configuration: a user-writable
file authoritative for a root process, or two readers drifting apart, are both worse
than a copy.

## Not done

- **First run does not start the service.** It writes the configuration and shows the
  command. On a packaged install systemd owns the unit, and a window shelling out to
  `sudo` at the moment a new user least understands the prompt is how an install
  becomes a support ticket.
- **Verified by running it**, twice: once in a browser against the mock (every tab,
  every config sub-section, a rule expanded into the chain editor), and once as the real
  Wails binary against a live service over the control socket — which is how the
  directory-deployment 500 and the spurious FIRST RUN were found. Both defects are fixed.
  What has NOT been done is a human judging the design, on the owner's screen, with real
  data. `?tab=rules` and `?fresh=1` exist for exactly that.

## Still to do

Flesh out the Nodes / Advanced tabs, and the switch/pin dropdown (no backend pin
endpoint yet).
