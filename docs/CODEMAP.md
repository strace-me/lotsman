# Code map

Dense per-package index of the Lotsman repo. Read this to rehydrate the whole
architecture without grepping source. For prose, see
[ARCHITECTURE.md](ARCHITECTURE.md); for direction, see [ROADMAP.md](ROADMAP.md)
and the GitHub issue tracker.

## What it is

Lotsman is a **self-healing anti-censorship router control plane** (NanoPi R5S,
OpenWrt). It watches services, probes them, and steers the data plane so the
internet keeps working under TSPU/DPI with no manual blockcheck + config edits.
Split: **Brain** decides *what* should be active (pure policy state machine);
**Applier** does *how* (Clash selector flips, nfqws strategy swaps); **Sapper** =
**Tester** (finds working strategies) + **KB** (EWMA memory of what works). The
**data plane** is four owned layers: capture (nft tproxy) → route (sing-box
selectors) → desync (nfqws/zapret on direct) → NAT. M0 = one daemon `lotsmand`;
the component split is mechanical (event contracts already separate them).

## Control loops (`pkg/events` channels in-proc; same shapes go over a bus later)

- **FEEDBACK**: prober → `ProductionVerdict` → Brain (+ KB records EWMA).
- **CONTROL**: Brain → `DesiredStateChanged` → Applier (selector / nfqws swap).
- **RECONCILE**: Applier → `ActualStateObserved` → Brain (drift → re-assert).
- **DISCOVERY**: Tester → `ExperimentCompleted` → KB (Sapper; sandbox, deferred).

## Packages

### Leaf / contracts
- `pkg/events` — cross-component message contract + in-proc bus. [ProductionVerdict, DesiredStateChanged, ActualStateObserved, Bus] [the network-split seam]
- `pkg/registry` — services, categories, denormalized strategy chain (STATE_CHAINS). [Service, Device, State, Position]
- `pkg/strategy` — strategy classes + cold-start seed order; leaf, no lotsman imports. [ClassZapret, ClassByeDPI, ClassVPN, ...]
- `pkg/config` — declarative YAML → registry + subscriptions + pools; composition root, assigns chain positions, validates. [Config, Parse]

### Data model / generation
- `pkg/subscription` — parse VPN subscriptions (several formats) into Nodes. [Node, Parse]
- `pkg/pools` — group nodes into named pools by capability + tag filters (pure membership). [Pool, Filter, Capability]
- `pkg/aggregate` — merge domain/IP lists from many sources → normalized dedup list; fetch behind interface; global shrink guard. [Result, Source, ParseList, ShrinkOK]
- `pkg/singbox` — generate full sing-box config from services/nodes/pools; Merge does surgical node-group injection. [Generate, Merge, NodeGroup, Options]
- `pkg/coherence` — reconcile routing model vs desync model: projects zapret-service domains into the nfqws hostlist, reports gaps it can't close (a zapret service sent direct only works if its domains are in the hostlist). [Analyze, Gap]
- `pkg/reconcile` — daemon OWNS the sing-box routing config: compute desired (singbox.Generate) vs live, on real diff `sing-box check` + swap + backup/rollback. Owns structure, not steering. Dry-run by default. [Reconciler, Reconcile]

### Data plane
- `pkg/dataplane` — Clash-API client (selector flip, pool reachability, /delay, /connections); ss tcp_info parsing. [ClashClient, SetSelector, Connection]
- `pkg/executor` — adapt a strategy class to a concrete action; one StrategyExecutor per class, idempotent Enable. [StrategyExecutor, ScriptSwitcher (symlink+restart, backs nfqws & byedpi), SelectorSetter]
- `pkg/applier` — converge production toward Brain's desired state; maps (class,strategy)→executor, runs idempotently, reports ActualState back (closes reconcile loop). Owns no policy. [Applier]
- `pkg/capture` — TM-2: own the tproxy nft table (`table ip singbox`); byte-identical GenerateNft + reconcile vs `nft list table`. [Model, DefaultModel, GenerateNft, Runner]
- `pkg/zapret` — model nfqws instances as the managed unit (one process / NFQUEUE / port set / switchable strategy). [Instance]
- `pkg/zaptune` — per-SERVICE recipe tuning glue: "service has blocked domains" → "its zapret rung uses recipe X"; keeps service as the unit (one recipe, one --new block, all its domains).
- `pkg/zapretgen` — LOT-10b: single writer of nfqws strategy; compose nfqws config for services currently on a zapret rung + (when armed) apply as one unit. nfqws-side analogue of reconcile. [Reconcile]
- `pkg/nfqwsgen` — compose nfqws config from registry: one `--new` block per zapret-class service scoped to its own domains+recipe; pure. nfqws-side projection of singbox.Generate.
- `pkg/rulesets` — Track A autoupdate: keep binary `.srs` rule-sets fresh under pin + autobump-with-validation (reuses aggregate shrink guard); IO behind interfaces, decision core pure.
- `pkg/flowseal` — install/update the Flowseal strategy bundle: extract release zip into versioned dir, repoint stable symlink; GitHub release polling. [FileInstaller, Release, CurrentVersion]

### Brain / probe
- `pkg/brain` — the policy: decide what should be active, emit DesiredStateChanged; touches no network. Asymmetric escalate/recover, settling window, silent recovery probes. [Brain, Smarts (intelligence hooks), escalateLocked, shouldEscalate, Position, Snapshot]
- `pkg/probing` — active probes on a per-service interval; publish verdicts, record EWMA into KB, rotate silent probes through lower positions when escalated. No policy. [probing.New]
- `pkg/pathhealth` — escalation-v2 DETECT: probe EVERY chain step in PARALLEL out-of-band (without flipping the live selector) → report which tiers work now, so ACT can jump to best. [Detector, Scan]

### Intelligence (wired into Brain via Smarts; runtime node/quality intel)
- `pkg/policy` — compose signals (systemic? settling/flapping?) into one escalate/hold/stay decision; pure core of "smart" escalation.
- `pkg/correlate` — systemic outage (many services down at once) vs local problem → hold, don't churn the chain. [Detector, Set, Summary]
- `pkg/damper` — suppress strategy flapping: count recent transitions, return exponential backoff added to the settling window. [Damper, Record, Count]
- `pkg/adaptive` — tune escalate/recover thresholds per service from observed reliability + flap count (flaky tolerates more; flapping must prove stability longer). [Tuner, Thresholds, Tune]
- `pkg/anomaly` — probe stream → degraded vs down (intermittent loss / latency spike), react before total outage. [Detector, State, Config]
- `pkg/tspu` — classify block type from failure mode (RST→DPI/zapret, timeout→IP-drop/VPN, fake DNS→poison, big RTT→throttle); pure. Suggests strategy class.
- `pkg/quality` — connection-quality metrics (p50/p95/p99 tail, jitter, loss) from RTT samples or passive tcp_info; one vocabulary for both. [Quality, FromRTTs]
- `pkg/balancer` — rank VPN nodes/pools by quality with per-category weights (voice=jitter/loss, gaming=tail, streaming=bw). [Weights, ProfileFor, Candidate]
- `pkg/vpnbalance` — actively health-check concrete nodes behind a selector (Clash /delay), rank via balancer, repoint to best live node (fixes url-test sticking to a dead pick). [Rebalance]
- `pkg/scoring` — kb.Stats → single comparable number, weighted per category (latency alone misleads). **DEFERRED: build-ahead, not wired.**
- `pkg/affinity` — `Spread` is LIVE: deterministic per-client node spreading (LOT-23), called by `singbox.Generate`. The sticky-store half (`Store`/`Resolve` with TTL) is DEFERRED. [Spread (live), Store/Resolve (deferred)]
- `pkg/ttl` — estimate hops-to-DPI for accurate fake-packet TTL (--dpi-desync-ttl); traceroute parser fallback, math pure. **DEFERRED: build-ahead, not wired.**
- `pkg/noderank` — pick the best concrete VPN node FOR A SERVICE and pin its selector; narrows by exit country BEFORE probing (never pins a blocked service to a RU exit). [noderank.New]
- `pkg/selector` — choose which strategy to try next: prefer the class addressing the detected block type (tspu) + best learned score (KB). **DEFERRED: superseded in practice by brain+kb; build-ahead reference.** [Candidate]
- `pkg/tuner` — decision core of the "auto" knob mode: A/B OFF vs ON → keep ON only if it measurably helps (significance margin + hysteresis); pure. **DEFERRED: build-ahead, not wired.**

### Self-heal (LOT-15..34 epic)
- `pkg/observe` — passive "eye" (LOT-15): read sing-box /connections, compute per-service real-traffic metrics in Go; OBSERVE only (surfaces misrouting: flow that should ride sel-<svc> but went direct). [observe.New]
- `pkg/misroute` — LOT-16 runtime-coherence DETECT: observe snapshot → per-service misroute verdict (leak-to-direct, or matched UDP/QUIC flows stalling/dead); pure. [Detect, Verdict]
- `pkg/iplearn` — learn destination CIDRs to widen route sets; Coalesce reduces a CIDR set (drop contained, merge siblings to covering parent, never over-widen). [Coalesce]
- `pkg/bypasslearn` — TM-5: watch live flows, learn NAT-sensitive destinations (game-UDP, voice/RTC, bidirectional) → treatment=bypass set by traffic CLASS, never by device. [Classifier, Candidate]
- `pkg/remediate` — LOT-18a self-heal PLANNER: pure misroute Verdict → staged Plan (which ladder rung); PROPOSE-ONLY, no I/O. [Plan]
- `pkg/remctl` — LOT-18b ARMED remediation controller: state machine that mutates the live sing-box config — gated, hysteresis, canary verify, auto-rollback. Highest-stakes; apply/rollback/verdicts injected. [Controller, DefaultConfig, HotActions (LOT-34 rule_set toggle, no restart), NewActiveSet]
- `pkg/incident` — persist remediation lifecycle (detected→applied→resolved/rolledback/escalated) as JSONL; audit trail for the armed ladder. [Incident]
- `pkg/enginehealth` — LOT-33 data-plane engine watchdog: detect WEDGED engine (sing-box can't route, nfqws not desyncing) and restart — but ONLY when the WAN gate is up (makes boot order irrelevant, avoids restart storms). Probes/restarts injected. [Check, Run]

### Generator / discovery / tester (Sapper)
- `pkg/desyncgen` — LOT-31/v7 engine-AGNOSTIC core: model desync param space as Axes; produce candidate Strategies via Grid (cold sweep) or Mutate (one-axis neighbours of a working seed). Add an engine = supply Axis set + Render. [Axis, Strategy, Engine]
- `pkg/desynctune` — LOT-31 v7c tuner: turn generated strategies into an A/B, ask tester which beats the no-desync baseline against the live service. Apply + probe injected. **BUILD-AHEAD: the generator/tuner arm (desynctune/tester/burstprobe + zapret.NfqwsEngine) is not wired into the daemon yet (v7 tuner epic).** [Candidates, StrategyID]
- `pkg/blockcheck` — Sapper discovery arm: drive zapret blockcheck.sh, parse working strategies (SUMMARY + "working strategy found"); ParseEnumerated harvests the full tried search space. [ParseSummary, ParseEnumerated, Dedup, Result]
- `pkg/domainscan` — "which domains of a rule actually work from the box?" probe each over the box's own (nfqws-traversing) path; pure Verdict (only timeout/reset = DPI block). [Classify, Verdict]
- `pkg/strategycat` — curated structured catalog of zapret/nfqws desync recipes (one `--new` block each); sourced from Flowseal/StressOzz, dedup by normalized args. [catalog.yaml]
- `pkg/strategyimport` — read third-party strategy bundles (Flowseal `.bat`, shell, fenced markdown) into catalog recipes: one normalizer over all containers since winws and nfqws share the flag family; deployment detail dropped, payload paths to basenames, host selectors to `{{DOMAINS}}`, `--ipset` refused whole rather than stripped. IDs derive from the normalized args so the KB keeps its learning across bundle updates. **Not yet wired to `pkg/flowseal`** — an update still does not re-read what it brought. [Import, ImportDir, Normalize, Techniques]
- `pkg/stunprobe` — measure a UDP "voice" path via STUN Binding Requests (voice is raw UDP you can't curl; STUN is what nfqws `--filter-l7=stun` mangles).
- `pkg/tester` — decision core of the zapret strategy tester; an ADVISOR to the zapret rung (Brain owns the ladder, applier owns the data plane). **BUILD-AHEAD: not wired yet (v7 tuner epic).** [Probe]
- `pkg/burstprobe` — sustained-read (throughput) probe feeding tester's throughput fitness: pulls real content past the ~16KB TSPU freeze cliff and reports worst-case goodput across endpoints. READ-only; http.Client injected. **BUILD-AHEAD: not wired yet (v7 tuner epic).** [Probe]

### Ops / persistence / maintenance
- `pkg/metrics` — Prometheus text exposition on /metrics (NetData-scrapeable): Brain positions, KB EWMA, observed probe-outcome counts; no external lib. [metrics.New]
- `pkg/audit` — persist Brain state TRANSITIONS (what switched, when, why) as JSONL; survives restarts. [Transition, Recorder, FileRecorder, Nop]
- `pkg/faillog` — persist individual probe FAILURES as JSONL (distinct from audit: services fail without transitioning). [Failure, Recorder, FileRecorder]
- `pkg/state` — persist Brain per-service chain positions (atomic JSON) so a restart resumes instead of resetting everyone to PREFERRED. [state load/save]
- `pkg/kb` — strategy memory: in-mem EWMA success rate per (service, strategy), rank by it, fall back to cold-start seed; `--kb-file` persistence. [kb.New, Snapshot, Stats]
- `pkg/periodic` — run background maintenance tasks each on its own interval/goroutine; a failing task logs + retries next tick. [periodic.New, Task, Add]

`cmd/lotsmand/main.go` — the daemon: builds the bus + KB, wires Brain (+ Smarts:
correlate/damper/adaptive/anomaly), Applier (execs), probing engine, metrics, and
gated periodic groups (reconcile, vpnbalance, noderank, zapretgen, rulesets,
kb-save, observe→remctl, enginehealth, pathhealth). Most self-heal/apply paths are
flag-gated and dry-run by default.

## Where to change X

- **Add a desync engine (nfqws/byedpi/youtube-unblock)** → implement `desyncgen.Engine` (Axes + Render); generation/probing/ranking is reused.
- **Add a strategy class / new data-plane action** → `pkg/executor` (`StrategyExecutor`), then register in main wiring.
- **Add a remediation rung** → `pkg/remediate` (planner) + `pkg/remctl` (armed apply/rollback); record in `pkg/incident`.
- **Change escalation behavior** → `brain.escalateLocked` / `brain.shouldEscalate`; parallel-tier detect in `pkg/pathhealth`; signals via `brain.Smarts`.
- **Change nft capture / NAT** → `pkg/capture` (Model + GenerateNft).
- **Change what sing-box config looks like** → `pkg/singbox.Generate`; ownership/apply in `pkg/reconcile`.
- **Change nfqws strategy output** → `pkg/nfqwsgen` (render) + `pkg/zaptune` (recipe pick) + `pkg/zapretgen` (single writer).
- **Add a background maintenance job** → `periodic.Task` in main.
- **Add a node/quality ranking signal** → `pkg/quality` + `pkg/balancer` weights; consumed by `vpnbalance`/`noderank`.

## Dependencies (intra-module; AUTO — don't hand-edit; check before adding code to avoid duplication)
Regenerate: `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... | sed -E 's#github.com/strace-me/lotsman/##g' | awk '{printf "%s:",$1;for(i=2;i<=NF;i++)if($i~/^pkg\//){sub(/^pkg\//,"",$i);printf " %s",$i}print ""}' | grep -E '^pkg/|^cmd/' | sed -E 's#^pkg/##' | sort`

```
aggregate: subscription          applier: events executor          balancer: quality
blockcheck: strategy             brain: anomaly audit events policy registry strategy
burstprobe: quality              coherence: registry strategy
config: aggregate dataplane pools registry strategy subscription zapret
dataplane: events quality registry strategy stunprobe   desynctune: desyncgen tester   executor: dataplane registry strategy
flowseal: subscription           kb: strategy
metrics: brain misroute noderank observe remediate subscription
misroute: observe                nfqwsgen: aggregate strategycat    noderank: balancer dataplane quality
observe: registry                pathhealth: dataplane registry strategy   policy: anomaly
pools: subscription              probing: dataplane events faillog kb registry
reconcile: pools registry singbox subscription
registry: strategy               remctl: incident misroute remediate singbox   remediate: iplearn misroute
rulesets: aggregate              scoring: kb   singbox: affinity pools registry subscription   stunprobe: quality
tester: quality                  tspu: strategy   tuner: quality   vpnbalance: balancer dataplane quality
zapret: desyncgen strategy       zapretgen: registry strategy strategycat zapret zaptune
zaptune: nfqwsgen registry strategy strategycat
```
(leaf, no intra-deps: adaptive affinity anomaly audit bypasslearn capture correlate damper desyncgen
domainscan enginehealth events faillog incident iplearn periodic selector state strategy
strategycat subscription quality ttl). `cmd/lotsmand` wires ~all; `cmd/lotsmanctl` = blockcheck
coherence config dataplane domainscan registry singbox strategy subscription.

**Anti-duplication rule:** before writing a new helper, grep the target pkg + check this graph — if a
pkg already does it (e.g. composition→zaptune/nfqwsgen, NAT-sensitive classify→bypasslearn, .srs→domains
→rulesets, node rank→noderank), reuse it.

### Client (`client/`, desktop)

Single-device Lotsman. Composes the spine above unchanged — see [CLIENT.md](CLIENT.md)
for how to run it and the operational facts. Desktop shape is a privileged service
plus a thin unprivileged UI, since only the tun and the NFQUEUE rules need root.

- `client/core` — spine composition (bus/kb/brain/applier/probing), client config
  generation, preflight refusals, subscription reconcile, box supervisor, canary
  verdicts into the KB, the rich `/status` report, domain-pack refresh, DNS
  auto-failover, a per-network KB that follows a roam, and the unix control
  socket — group-ownable (0660) so an unprivileged UI can drive a root service,
  owner-only 0600 by default.
- `client/control` — dependency-light client for that socket; the window and the
  tray talk to the service ONLY through this, so an unprivileged UI never links
  the engine.
- `client/platform/externalbox` — `ProxyCore` over an external sing-box run as a
  managed child; pid recorded so a SIGKILLed client's orphan is reclaimed.
- `client/platform/nfqws` — zapret desync on Linux: nft NFQUEUE rules via
  `zapret.GenerateNft` (same renderer the router's init script uses) plus nfqws as
  a child taking argv, not a generated shell line.
- `client/platform/rulesets` — provisions the `.srs` the config references from the
  same upstream bundle the router's updater consumes, and decompiles them back to
  domains so the desync covers what sing-box routes.
- `client/platform/hostdns` — point the host's OWN resolver at an in-tun sentinel
  so its DNS stops escaping past the deliberately-excluded LAN resolver; the
  original is stashed in a sidecar so even a crash restores it. Linux+root+tun,
  off by default.
- `client/platform/netid` — short stable id for the attached network (the default
  gateway's MAC, with fallbacks), so the KB is kept per network instead of pooled.
- `client/scaffold` — write a working starter config from nothing but a
  subscription URL, or ship the curated `recommended.yaml`; first-run onboarding.
- `client/desktop` — headless entrypoint (the privileged service).
- `client/gui` — the desktop window (Wails v2 + Svelte), its own module, linking
  only `client/control`: dashboard plus configurator sections (services,
  subscriptions, DNS, engines, strategies, lists).
- `client/tray` — the systray companion, a separate unprivileged process and its
  own module: polls `/status`, recolours the sextant by verdict, Open / Stop / Quit.
- `client/mobile` — the only surface Kotlin touches, bound with gomobile over
  libbox; its own module so sing-box's dependency graph never reaches the root
  one. The VpnService shell lives in `android/`.

## Other durable state

- [docs/ARCHITECTURE.md](ARCHITECTURE.md) — prose architecture, state machine, routing model.
- [docs/ROADMAP.md](ROADMAP.md) — direction and what's not wired yet; open work lives in the GitHub issue tracker.
- [docs/CLIENT.md](CLIENT.md) — the desktop client: running it, host requirements, and the facts that cost the most to learn.
