# Changelog

All notable changes to Lotsman. Format loosely follows [Keep a Changelog]; dates are
the branch's working dates, not tagged releases (nothing is tagged/released yet).

## [Unreleased] — branch `prerelease-fixes`

The desktop-client track and pre-release engine hardening. 79 commits ahead of `main`.
Everything below is validated live on a NixOS ThinkPad (sing-box 1.13.14) unless noted.
See `docs/DESIGN-client-ui.md` and `docs/DESIGN-dns-and-tun.md` for design + handoff.

### Desktop client (GUI + tray + control plane)

- **Unprivileged GUI** (Wails v2 + Svelte, `client/gui`) and a **separate tray process**
  (`client/tray`, fyne/systray), talking to the service over a UNIX control socket
  (`client/control`). All state stays in the service; the UI only renders + issues
  actions. Five tabs: Обзор / Ноды / Подписки / Конфиг / Ещё. Sextant logo + mono tray
  glyph.
- **Rich `GET /status`** — a backward-compatible superset of `{running, services}` with an
  honest verdict (gated on real data-plane liveness + brain counts, never config),
  per-engine *measured* liveness, network fingerprint, fleet + subscription quota.
- **Actions** — `POST /service/{name}/recheck`, `GET /events` (brain rung-transition ring).
- **In-app configurator** — the daemon owns the YAML; the GUI edits *structure* (a
  round-trippable `config.Document`) and posts the whole doc, with a raw-YAML escape
  hatch. `GET/POST /config` + `/config/validate` always validate before writing.
- **Config apply without dropping the connection** — `POST /config` applies via in-place
  `Core.Reload` (rebuilds the autonomy loop, restarts sing-box only on a real diff), so a
  non-routing edit keeps every live connection and a routing edit never re-execs the
  daemon; re-exec is the fallback. (`488ae04`)
- **`-init -recommended`** — a curated default config (services + rules, no creds) for the
  first-run "no subscription yet" state.
- **In-app zoom** (Ctrl +/−/0, Ctrl+wheel, persisted) and a `-scale` flag for HiDPI.

### GUI rendering on Wayland (wlroots/Hyprland)

- **XWayland fallback** (`724aac0`) — WebKitGTK computed a negative devicePixelRatio and a
  multi-billion-pixel viewport under Wayland at a fractional/2× scale, collapsing the
  layout. Route the webview through XWayland (`GDK_BACKEND=x11`) when on Wayland; native
  X11 is untouched. Not fixable via Wails version, env, or webkit flags (a GTK3/webkit2gtk
  limitation). GTK4 (the real fix) researched + deferred to a future Wails-v3 spike.
- Fixed a null-iteration crash that blanked the default Обзор tab (`09d28a0`).

### Tun mode + split-DNS

- **Split-DNS generator** (`pkg/singbox/dns.go`) — the sing-box 1.12+ type-based DNS format
  (gated by `FeatureDNSServers` ≥ 1.12). Transports udp/tcp/**tls (DoT)/https (DoH)/quic
  (DoQ)/h3 (DoH3)**/local + fakeip, each pinnable to an outbound via `detour`. Censored
  domains resolve via an encrypted resolver **detoured through the VPN** (unblockable +
  unpoisoned); the OS resolver serves RU-direct + bootstrap. (`d32d1d9`)
- **User-configurable DNS** (`pkg/config/dns.go`) — a `dns:` block of servers by curated
  provider alias (`{provider: cloudflare, method: tls}`) or fully manual, plus
  direct/final/strategy/fakeip. Catalog: cloudflare, quad9, google, adguard, mullvad
  (IP + SNI, endpoints verified live). Validated. (`ff377c5`)
- **`hijack-dns`** route rule (tun + DNS + ≥1.12) hijacks captured DNS into the trusted
  resolver. (`8253089`)
- **Three tun bugs fixed** (found by the first end-to-end battle test):
  - LAN excluded from `auto_route` so the tun no longer swallows the LAN and kills SSH
    (`9410016`).
  - nfqws runs with `cmd.Dir` = the zapret-files dir so its bare fake-payload filenames
    resolve — desync now arms (`2a5e3e7`).
  - `route.default_domain_resolver` emitted with the DNS block, else sing-box 1.12 rejects
    the config (`e839678`).
- **Battle test passed** — capture → route → **desync/nfqws** → NAT end-to-end on the
  ThinkPad; SSH survived; the self-heal brain armed, canary-tested, and escalated zapret
  recipes to a converged 5/6.

### Engine / self-heal / networking (earlier in the branch)

- Per-network knowledge base (netid + `-kb-dir`), roaming (swap the KB on a network
  change, `-roam-interval`), within-network cross-service desync prior.
- Learn which desync strategy beats the DPI in front of each service; skip recipes whose
  payloads this host lacks; probe inactive rungs directly, not through the selector.
- Survive a dead/restarted/hard-killed sing-box; keep local discovery off the tunnel.
- Back-fill an empty VPN pool so UDP services (gaming/messaging on a VLESS-only fleet)
  don't 404 (`30115ff`); list configured subscriptions even without a Userinfo header
  (`e68d314`).
- Units, config scaffold, observability, cross-platform build, and a batch of LOT fixes
  hardened by adversarial review rounds.

### Known gaps (see `docs/DESIGN-dns-and-tun.md`)

- The **host's own DNS** (its shell/browser) for domains the ISP NXDOMAINs still escapes
  to the ISP — closing it needs system-DNS management (point resolv.conf at a captured
  resolver, restore on stop). SNI-routing saves most TLS in the meantime.
- **Daemon obvyazka** — running Lotsman as a root systemd daemon at boot with a
  group-readable socket (+ polkit) so the unprivileged GUI controls it; required to
  daily-drive Lotsman in place of the bare system sing-box.

[Keep a Changelog]: https://keepachangelog.com/
