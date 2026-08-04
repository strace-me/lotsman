# Changelog

All notable changes to Lotsman. Format loosely follows [Keep a Changelog]; dates are
the branch's working dates, not tagged releases (nothing is tagged/released yet).

## [Unreleased] — branch `prerelease-fixes`

The desktop-client track and pre-release engine hardening. 81 commits ahead of `main`.
Everything below is validated live on a NixOS ThinkPad (sing-box 1.13.14) unless noted;
the daily-driver obvyazka below (group socket + host-DNS) is built + unit-tested but its
live root test is still pending. See `docs/DESIGN-client-ui.md` and
`docs/DESIGN-dns-and-tun.md` for design + handoff.

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

### Daily driver — proven live, and the failures found by proving it

Both root tests passed on the ThinkPad, and running them found more than they were
written to check. See `docs/HANDOFF-2026-08-04.md`.

- **host-DNS works, and the sentinel had to be the tun PEER** — a packet to the tun's own
  address is delivered locally and never reaches sing-box to be hijacked. Proven by four
  ISP-NXDOMAINed domains resolving through the tunnel, with `resolv.conf` restored
  byte-identically on stop.
- **Roaming re-capture** — the host's resolver and the machine's excluded subnets were
  captured once at startup and then carried forever, so "close the lid at home, open it
  in a café" left both DNS exits dead while every health signal stayed green. Roaming now
  re-captures both and regenerates through a *fresh* reconciler (the running one had
  snapshotted its options). `Redirect`/`Restore` decide from the file rather than an
  in-memory flag, so a network manager rewriting `resolv.conf` no longer causes either a
  silent leak or — when switching Lotsman off — a clobbered resolver.
- **A watchdog for the case nothing else can see**: the tunnel up, the Clash API
  answering, and DNS going nowhere. Three unanswered probes hand the host's own resolver
  back.
- **nfqws was being restarted 78 times in two minutes.** `brain.Snapshot()` iterated a
  map, so Go reshuffled the service order on every call, the composed argv differed each
  tick, and `Apply` read that as a strategy change — with the canary then judging recipes
  against an engine that had just restarted. Sorting at the source fixed every consumer.
- **A NixOS module** (`examples/nixos-lotsman-service.nix`) — `/etc/systemd/system` is a
  read-only symlink farm there, so the unit must be declared; a service declared by
  another module cannot be `systemctl disable`d, so the system sing-box's `wantedBy` is
  overridden instead. `Restart=on-failure`, because with `always` the tray's off button
  would be undone ten seconds later.
- **The service was missing a sixth of the desync catalog**: it pointed at nixpkgs'
  zapret, which ships 31 payloads, while 11 of 67 recipes need three Flowseal-derived
  ones. Those are now vendored in `assets/zapret-payloads/` and merged into one directory
  at start.

### Error-handling audit — six root causes

A 16-agent adversarial audit found the confirmed defects were not independent bugs.

- **"Applied" now means applied.** `Reconcile` returned bare `nil` on paths that wrote
  nothing (degraded fetch, dry-run), so every caller read "skipped" as "applied";
  `ErrDeferred` had existed for exactly this reason on a third path and was never
  generalised. `Reload` was a teardown followed by a fallible step with no rebuild, so any
  reconcile error left the client with a live tunnel and nothing steering it — painted
  green, because the box was alive and the Clash API answered.
- **Bookkeeping committed before the effect it recorded**: the DNS failover loop counted
  rotations before `Reload` confirmed anything, so a run of skipped reloads walked memory
  through the whole provider list while sing-box still ran the first one — then concluded
  every provider had failed.
- **A failed re-exec exited 0**, so `Restart=on-failure` saw a clean shutdown after the
  data plane was already down. The non-unix path now starts a fresh process rather than
  claiming a service manager will.
- **Liveness of a process taken as health of a subsystem** — `superviseBox` restarted a
  wedged box, then declared it healthy on the next tick because the process existed.
- **A compensating action's failure discarded along with the handle to retry it** — the
  host-DNS manager was dropped even when its restore had failed, leaving `resolv.conf`
  pointing at a dead sentinel with nothing able to put it back.
- **A transient miss amputating a rung forever**: `dropUnsupportedRungs` wrote its trimmed
  chains into the shared registry, so a rung dropped once because its executor happened to
  be unavailable — the desync gives up when there is no default route, exactly a laptop's
  state after a resume — never came back.

### Config and lists

- **Domain packs** — `hostlists:` declares a pack once (sources, excludes, shrink guard)
  and `domain_lists:` attaches it to any service, so both engines get the same domains.
  Wildcards are normalised (`*.foo.com` as a `domain_suffix` matches nothing), dedup is
  case-insensitive, duplicate pack names are rejected, and a pack is bounded at 50k
  domains because these are emitted inline.
- **A failed *exclude*-source fetch used to make a list GROW**, sailing past both guards
  and silently putting back the domains it was asked to leave out.
- **Strict YAML** (`KnownFields`): a misspelled key was indistinguishable from an absent
  one, and the in-app editor would then rewrite the file *without* it.
- **The client silently dropped half its config knobs** — `utls_fingerprint`, `multiplex`,
  `fakeip` and `singbox_version` were honoured by the daemon and ignored by the client;
  the first two are precisely the anti-fingerprinting and anti-parallel-handshake levers.
- A service that matches nothing no longer disarms the desync for the whole fleet:
  "nothing to cover" and "could not be covered" are different outcomes.

### GUI

Structured editors for **DNS**, **Движки** (uTLS/multiplex/fakeip/version), **Стратегии**
(custom nfqws recipes) and **Списки** (domain packs, with a live cross-reference of which
services use each). All Mac-validated in mock mode; a Go round-trip test pins the
PascalCase keys the forms bind against the config structs.

### Android and Windows

- **Android Stage-1 scaffolding**: `client/mobile` (its own module, so sing-box's
  dependency graph stays out of the root one) with an `AndroidProxyCore` over libbox, plus
  a Kotlin/VpnService shell. The Go half **builds for android/arm64, arm and amd64** — and
  established that the build tags are mandatory, or the app links a clash-server stub and
  every start fails. Nothing Kotlin has been compiled.
- **Windows**: `GOOS=windows go vet` had never passed (a test used `syscall.Stat_t`), so
  the package with the most Windows-sensitive logic could not compile its own tests there.
  CI now cross-builds and vets windows/darwin/android — the last because Go's `linux`
  build tag silently matches android, which had already let two desktop-only files into an
  Android build.

### Daily-driver obvyazka — group socket + host-DNS

The two pieces needed to run Lotsman as the ThinkPad's VPN in place of the bare system
sing-box (see `docs/DESIGN-dns-and-tun.md`):

- **Group-owned control socket** (`876e694`) — `-control-socket-group <group>` makes the
  control socket group-owned `0660` and its directory group-traversable `0750`, so an
  unprivileged GUI/tray in that group can drive a root systemd service without being
  root; an unknown group fails loudly. `DefaultSocketPath` now falls back to the system
  socket (`/run/lotsman/control.sock`) so the UI finds a root service with no `-socket`
  flag. The systemd unit gains `Group=lotsman` + `RuntimeDirectoryMode=0750` + the flag;
  a polkit rule (`examples/lotsman-client.rules`) lets the group start/stop the unit
  without a password. This is the mechanism the unit's own NOTE called "the missing piece".
- **Host-DNS redirect** (`6b828ba`, `-host-dns`) — with a tun up, the host's own
  browser/shell resolve censored names through the trusted in-tunnel resolver instead of
  leaking to the excluded LAN resolver the ISP can NXDOMAIN-poison (routed services
  already got un-poisoned DNS via DoH-over-VPN; this extends it to the host). New
  `client/platform/hostdns` rewrites `/etc/resolv.conf` to point at the tun and restores
  it on stop, crash-safe via a sidecar; sing-box's `direct` server is pinned to the real
  LAN resolver first so its own bootstrap does not re-read the redirected file and loop.
  Hardened against an adversarial review that found four host-strands-without-DNS paths:
  point at the tun PEER not its own address (a packet to the interface's own IP is
  delivered locally, never hijacked); **verify** a lookup actually resolves through the
  sentinel after redirecting and **revert** if not; `superviseBox` restores the resolver
  while sing-box is down and re-redirects when it is back; refuse a symlinked resolv.conf
  and gate crash-recovery on the live file actually being our sentinel. Off by default.

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

- The daily-driver obvyazka (group socket + host-DNS) and the **DNS auto-failover loop**
  (`dns.failover`: probe the active resolver through the tun, rotate `dns.final` to the
  next catalogued provider on repeated failure, apply via `Core.Reload`) are **built but
  not yet live-root-tested** on the ThinkPad. Open empirical questions: does sing-box
  hijack DNS sent to the tun peer `172.19.0.2` (host-DNS `Verify` reverts safely if not),
  and does the failover probe stay quiet without false-triggering. A structured DNS
  editor (`DnsEdit.svelte`) makes the block editable in the GUI meanwhile.
- **Exit / ingress-IP diversity** — the decisive lever against the measured TSPU killers
  (destination IP/CIDR/ASN reputation + the per-connection ~16 KB volume freeze), which
  desync and obfuscation do not beat. Still unowned; research in the memory notes.

[Keep a Changelog]: https://keepachangelog.com/
