# Lotsman & Sapper

Self-healing routing control plane for an anti-censorship home router (OpenWrt /
NanoPi R5S). It watches services, probes them, and automatically switches the
bypass strategy (ZAPRET / VPN / direct) when something stops working — so the
router fixes itself instead of needing manual `blockcheck` + config edits.

Two products in one monorepo:

- **Lotsman** (pilot) — orchestration: probes, state machine, switches sing-box
  selectors via Clash API, manages nfqws (ZAPRET) strategy lifecycle.
- **Sapper** (digger) — strategy intelligence: KB of what works where, EWMA
  ranking, blockcheck/discovery. *Tagline: Lotsman pilots through DPI waters,
  Sapper digs the channels.*

In M0 everything runs in one in-process daemon (`lotsmand`); the Lotsman/Sapper
split is mechanical (event contracts already separate the components) and happens
when standalone Sapper is actually needed.

Status: **pre-release, local only (not on GitHub yet).** Go 1.26, MIT-intended.
Builds to a static ARM64 binary (no CGO). The full intelligence pipeline is
validated against the real R5S in dry-run.

## Docs

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — components, loops, reconcile, package map
- [docs/ROUTING.md](docs/ROUTING.md) — the rule/group model, node allocation, mined traffic facts, optimization backlog
- [docs/CONFIG.md](docs/CONFIG.md) — full YAML + flags reference
- [docs/SOURCES.md](docs/SOURCES.md) — upstream projects, datasets, and references (attribution)

## Build / test

```sh
go build ./...                 # build everything
go test ./...                  # unit tests (all pure logic, no hardware)
go test -race ./...            # race check
# cross-compile for the router (NanoPi R5S, aarch64):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/lotsmand-arm64  ./cmd/lotsmand
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/lotsmanctl-arm64 ./cmd/lotsmanctl
```

## Run

```sh
# Demo on any machine (scripted prober, logs only, shows escalate->recover):
lotsmand -simulate -interval=500ms -duration=28s

# On the router, observe real decisions without changing anything:
lotsmand -config=/etc/lotsman/config.yaml -dry-run -smart -interval=30s \
         -metrics-addr=127.0.0.1:9101 -audit-log=/root/lotsman/transitions.jsonl

# Generate a sing-box config from config + live subscriptions, then validate:
lotsmanctl generate -config /etc/lotsman/config.yaml -out /tmp/sb.json
sing-box check -c /tmp/sb.json

# Or inject subscription nodes into the EXISTING (hand-tuned) router config,
# one url-test pool per subscription, without regenerating everything:
lotsmanctl merge -config /etc/lotsman/config.yaml \
                 -singbox-config /etc/sing-box/config.json -out /tmp/sb.json
sing-box check -c /tmp/sb.json   # then swap in with a backup

# Harvest zapret's strategy search space into catalog definitions (run on router;
# SIMULATE by default = no data-plane touch):
lotsmanctl harvest -script /opt/zapret/blockcheck.sh -domains rutracker.org -scanlevel force
```

### `lotsmand` flags

| Flag | Meaning |
|---|---|
| `-config` | YAML (services/subscriptions/pools/zapret/devices); empty = builtin youtube |
| `-dry-run` | log data-plane actions instead of executing (default true) |
| `-simulate` | scripted prober timeline instead of real probes |
| `-smart` | enable the intelligence layer in escalation (default true) |
| `-interval` | probe period |
| `-check-interval` | background maintenance period (flowseal update, subs refresh, vpn-balance, hostlist rebuild); 0=off |
| `-probe-proxy` | SOCKS5 host:port (sing-box socks inbound) to route probes through, so box probes follow the LAN path; empty = probe direct |
| `-vpn-selector` | Clash selector tag to actively rebalance across its concrete nodes (needs `-check-interval` + a selector outbound); empty = off |
| `-vpn-probe-url` | URL for per-node Clash `/delay` health checks used by `-vpn-selector` |
| `-metrics-addr` | expose Prometheus `/metrics` (NetData scrapes it) |
| `-audit-log` | append state transitions as JSONL |
| `-fail-log` | append individual probe failures as JSONL (review what was failing during an outage); empty = off |
| `-state-file` | persist/restore chain positions across restarts |
| `-zapret-*` / `-byedpi-*` | script dir / active symlink / init service per engine |
| `-clash-base` | sing-box Clash API base URL (for VPN/Direct selector switching) |

## Architecture (4 components, event-driven)

```
prober ──ProductionVerdict──▶ Brain ──DesiredStateChanged──▶ Applier ──ActualStateObserved──▶ Brain
              │                  │ (consults KB + intelligence)        │ (executors: zapret/byedpi/vpn/direct)
              └──────────▶ KB (EWMA, latency, ranking)                 └──▶ sing-box (clash-api) / nfqws
```

- **Brain** (`pkg/brain`) — state machine (escalate/recover, settling, asymmetric
  thresholds) + reconciler (desired↔actual). Decides via the intelligence layer.
- **Applier** (`pkg/applier`) + **executors** (`pkg/executor`) — idempotent apply;
  `ScriptSwitcher` (zapret/byedpi: symlink swap + service restart), `VPN`/`Direct`
  (clash-api selector flip).
- **KB** (`pkg/kb`) — per-(service,strategy) EWMA success + latency + jitter, ranking.
- **prober** (`pkg/probing` + `pkg/dataplane`) — HTTP/TCP/STUN probes, burst
  (p95/p99/jitter via `pkg/quality`), passive metrics from `ss -ti` (`ParseSS`).

### Intelligence layer (the "smart" part)

When `-smart` is on, Brain's escalation runs a pipeline instead of a raw counter:

```
probe → anomaly(degraded/down) + correlate(systemic?) + damper(flap?) + adaptive(threshold from EWMA)
     → policy.Decide → escalate / hold / stay (+reason)
     → on escalate: tspu classifies the block (RST→zapret, timeout→VPN) → jump to that chain step
     → KB score (success − latency penalty) ranks the strategy
```

Packages: `policy` (composer), `correlate` (systemic vs local — don't churn on an
ISP outage), `damper` (anti-flap backoff), `adaptive` (per-service thresholds),
`anomaly` (degraded vs down), `tspu` (block-type → mechanism), `selector`,
`balancer` (node scoring by metrics with per-category weights), `affinity` (sticky
outbound), `ttl` (hops-to-DPI → fake-packet TTL).

### Supporting packages

`registry` (services/chain/devices), `strategy` (classes + cold-start seed),
`config` (YAML loader), `subscription` (5-format parsers + Manager + health),
`pools` (caps/tag filtering), `singbox` (config generator), `zapret` (nfqws
instance model A/B + nft/init generation), `flowseal` (auto-update of the Flowseal
strategy bundle), `vpnbalance` (active per-node health-check + selector failover),
`aggregate` (domain-list merge), `events` (bus), `audit`, `metrics`, `state`,
`periodic`.

### Probe placement (important)

The router's own traffic does **not** traverse sing-box (tproxy only catches
forwarded LAN traffic), so a probe issued straight from the box measures the
wrong path and reports blocked services as down. With `-probe-proxy` pointed at a
sing-box **socks inbound**, probes follow the exact VPN/direct+nfqws routing LAN
clients get. `pkg/dataplane/socks.go` is a tiny dependency-free SOCKS5 CONNECT
dialer used for HTTP and TCP probes (STUN/UDP stays direct).

## Config example

See `examples/config.yaml`. Services declare a fallback chain
(`PREFERRED → ALT_ZAPRET → VPN`), rule-sets, and probe type; `devices` give
per-source overrides; `zapret.instances` declare nfqws instances (one = global,
many = per-rule independent switching); `subscriptions`/`pools` feed VPN nodes.

## Target deployment (NanoPi R5S)

Data plane stays as-is (sing-box tproxy:7893 + AdGuard + nfqws); Lotsman is the
control plane on top. Validated live on the R5S:

- **ZAPRET self-heal:** `/etc/init.d/nfqws` runs `/opt/zapret-lotsman/active.sh`
  (Flowseal ALT12) on qnum 200; Lotsman switches strategy by repointing the symlink.
  Every applyable zapret strategy must have its `<script-dir>/<id>.sh` launcher — in
  real mode lotsmand refuses to start if one is missing (a dangling symlink would
  restart nfqws into a broken config). Builtin catalog: `alt11`, `alt12`; declare
  more under `strategies:` in config.
- **clash-api** (`experimental.clash_api`, `127.0.0.1:9090`) enabled — Lotsman reads
  node state and switches selectors.
- **socks inbound** (`probe-in`, `127.0.0.1:7891`) — run with
  `-probe-proxy=127.0.0.1:7891` so probes follow the LAN path.
- **VPN node failover:** a `selector` outbound (`vpn` → `[vpn-pool, node-a, node-b]`)
  with route rules pointed at it; `-vpn-selector=vpn` makes Lotsman actively probe
  each concrete node (`/delay`), rank with `balancer`, and pin the best live one —
  closing the gap where sing-box url-test sticks to a dead node.
- **Autostart (procd):** `examples/lotsmand.init` → `/etc/init.d/lotsmand` runs the
  daemon 24/7 (respawn on crash, logs to `logread`), flags overridable in
  `/opt/lotsman/lotsmand.env`. Ships defaulting to `-dry-run=1` (observe only) —
  flip to `0` once trusted.
- **Merged hostlists:** the `hostlists:` config section rebuilds nfqws domain lists
  on `-check-interval` (fetch sources, drop exclude list, dedup, atomic write).
- **VPN pool from subscriptions:** `lotsmanctl merge -config <yaml> -singbox-config
  <live.json>` surgically injects subscription nodes into the live config — one
  url-test pool per subscription (`<sub>-pool`, tolerance 50ms), points the `vpn`
  selector at them, preserves everything else (idempotent). `-udp-mode`:
  `primary-fallback` (default; UDP → url-test over native hy2/tuic + VLESS pools),
  `split` (UDP → native only), `unified` (no split, VLESS carries UDP via xudp).
  Validate the output with `sing-box check` before swapping it in.

## What's not wired yet

- `affinity` into runtime VPN-node selection (sticky-per-client; `balancer` is wired
  via `vpnbalance`).
- `blockcheck` discovery → KB. Built so far (all mac-tested):
  - **Parse:** `blockcheck.sh`'s SUMMARY/working-strategy output (`ParseSummary`/
    `ParseWorking`) and the *full* search space it enumerates (`ParseEnumerated`),
    validated against a real SIMULATE run (`testdata/sim_quick_rutracker.txt`).
  - **Harvest:** `lotsmanctl harvest` runs blockcheck (SIMULATE/force, no data-plane
    touch) and imports the distinct nfqws strategies into the catalog as
    `strategy.Definition`s with deterministic `disc-<mode>-<hash>` IDs — acquiring
    zapret's curated strategy list without re-deriving it. **Run on the router**
    (needs the full zapret tree).
  - **Render:** `zapret.RenderLauncher` turns a discovered single-profile strategy
    into an `<id>.sh` launcher in Flowseal's shape (output passes `sh -n`).
  Still to wire on the box: the **isolation** step (a real, non-simulated run needs
  the production nfqws stopped or the target excluded from its nft rule, else it
  contaminates the test) and feeding found strategies into KB ranking. A real
  discovery run disrupts the live data plane — do it deliberately, in a window.
- subscription `Userinfo` (quota/expiry) into the periodic loop.
