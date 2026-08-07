# Changelog

All notable changes to Lotsman. Format loosely follows [Keep a Changelog]. Releases
are tagged from v6.13 on; everything before that was a build-time string, so the
older sections carry working dates rather than release dates.

## [Unreleased]

### The throughput canary had never run in steady state

- **It only ever fired on a transition.** `judge()` is called from `Enable` —
  three seconds after a rule ENTERS a desync rung — and `Reconcile` deliberately
  takes no verdict, so nothing measured volume while a rule sat still. Which is
  nearly always. `StallReason` therefore had nothing to report, and the probing
  engine read that silence as "not stalled". Caught the way everything here gets
  caught: YouTube would not load for hours on the owner's laptop, curl timed out
  three times out of three, and the journal held **not one canary line**. The
  branches were fine — a refused fetch lands squarely in "did not complete at
  all", and there is now a test proving it — the caller simply did not exist.
  A sweep now re-measures every 5 minutes.
- **`goodputOK` could not say whether it had measured anything.** Disabled, no
  target, a live voice call, an endpoint smaller than the ask — all returned the
  same bare `true` as a genuine pass. Harmless for a one-shot; wrong for anything
  periodic, where a sweep that declined would clear a real verdict taken minutes
  earlier and take its escalation with it. It now reports `measured`, and only a
  measurement that happened files a verdict.
- **The canary's reason was composed by the reader, not the observer.** Every
  failure was announced to the brain as "the path connects but carries nothing",
  including one where the shallow probe never connected — untrue of precisely the
  failure in front of us, where YouTube stalls at the TLS handshake. The stage
  that observed it now supplies the sentence. Principle 1, third instance in the
  same file.
- The shipped scaffold had **no `volume_target` on any rule**, so on a fresh
  install this whole dimension was inert while reading as wired. youtube has one
  now, and startup logs which rules the sweep can judge and which it cannot.

### The probe told the truth and the app buried it — twice, both mine

- **The canary's verdict reached the active probe but not the silent one.** For a
  rule with two desync rungs the same path therefore read broken when probed
  actively and healthy when probed silently: active fails at rung 1, the silent
  probe of rung 0 passes, the brain recovers to 0, active fails at 0, escalates
  to 1… Every transition resets the failure counter, so `fails` never left 0, the
  rule never reached BROKEN, and the verdict stayed **green over a service
  carrying nothing**. Measured while the owner could not load YouTube: 54
  "carries nothing" failures in six minutes, `stalledRatio` 0.92, verdict 11/11.
  The verdict is about the rule's DESYNC, so it now applies to any probe of a
  desync rung. After the fix the same box reported an honest `partial 7/4/0`.
- **The activity veto counted bytes in aggregate.** Its own evidence line gave it
  away: `1110 KiB moved across 387 live flows` — 2.9 KiB per connection, which is
  not a service working but a browser thrashing, opening connection after
  connection and getting a few kilobytes out of each before the freeze kills it.
  The floor waved that through and suppressed a probe failure that was telling
  the truth: the false green the veto exists to prevent, produced by the veto.
  The floor is now asked per CONNECTION too, at 24 KiB — above the ~16 KiB point
  where the freeze bites.
- Both are the same lesson at different scales: **an aggregate hides the
  pathology it is made of, and a signal applied to one path but not its
  neighbour invents a disagreement the system will oscillate between.**

### Believing the traffic over the probe

- A synthetic probe fetches one URL over one protocol and was measured wrong in
  BOTH directions within a day: YouTube passed a 204 while carrying no video,
  Discord's gateway timed out for minutes while 3.5 MB of voice flowed through
  the same rule. `SetActivityOracle` vetoes a FAILED active probe when passive
  observation of the operator's own connections shows the rule genuinely moving
  traffic — narrowly: movement since the last pass, not merely live flows; not
  while most flows are frozen; never overruling a verdict the stall oracle
  produced, since that one is itself a measurement.

### The knowledge base could recommend what this machine cannot run

- Four rules had the brain asking for `alt12` — the ROUTER's script name, not a
  recipe in this catalog — while nfqws ran its own pick. Self-sustaining: the
  brain resolves the id, the prober records outcomes under it, the KB's
  confidence grows, the brain resolves it again, and nothing in that loop ever
  tries to render it. The client now narrows the KB's advice to ids it can build;
  an empty recipe set (no desync rung here) filters nothing.
- The warning about an unhonoured pin is announced once per state change rather
  than on every recompose. It was firing 124 times in two minutes, and on a box
  whose journal ring holds about six minutes that does not merely annoy — it
  destroys the evidence for everything else.

### Rules get their own tab, and the app stops calling them services

- «Правила» is a flat list — one line per rule with its name, its ladder
  (`zapret → zapret → vpn`), what it matches and what it is doing right now —
  opening on click. Everything a rule carries is real but rarely edited, and
  showing it all at once was unreadable at eleven rules.
- The chain left the raw-YAML hatch with it: reorderable steps, pool names
  offered from the config, and a note that a step whose class has no executor
  here is silently dropped at startup — which the EMERGENCY rung was for months.
- **Naming.** A "service" meant both the three PROCESSES Lotsman runs and the
  rules it steers, in the same document; the dashboard said «Сервисы: 9
  работают» about rules, when there are three services and all were running. The
  UI now says правила / сервисы / ноды / пулы. The config key stays `services:`:
  renaming it breaks every config and backup on both machines and wants its own
  decision.
- Rule order is shown as the EFFECTIVE order — priority first, file order only as
  the tie-break — because that is what decides matching. Showing file order, as
  first requested, would have been a claim about behaviour that does not hold.
- (The tab landed inside the probing-fix commit rather than its own; an emergency
  `git add -A` swept it in.)

### Pools, expiry, and a fleet you could not see

- `control.Fleet` did not decode `nodes`. The service had emitted the full list
  for a while, so «Ноды» rendered a single number against the real backend while
  looking complete against the mock — 53 exits the owner pays for, invisible.
- Pools had no editor at all: the one place deciding WHICH exits a rule may use
  was reachable only through the YAML hatch. Renaming a pool now follows every
  rule that references it, deleting names what will break first, and each pool
  says whether anything references it — one that nothing points at changes
  nothing, and there was no other way to notice.
- Subscriptions gained `expires:` for providers that send no
  `Subscription-Userinfo` header, and `/status` says which of the two a date came
  from. In a month nobody remembers whether a number was reported or typed.
- Strategies display the upstream's own name: `flowseal-…-664-max (ALT12)`.
  `preset` is set only where KNOWN — three ALT12 recipes by construction, plus
  one verified byte-for-byte against ALT12's general block — never guessed.
- The page no longer scrolls as one; each panel scrolls in its own frame. The
  read-only Подписки tab is gone: three places to see them, one to change them,
  and the middle one did neither.


### Discord voice, and the three ways we were guaranteeing it could not work

- Voice media is raw UDP to whichever cloud Discord parked the voice server on
  that minute — measured: `35.217.47.243:50007` on Google Cloud, and
  `*.discord.media` resolving into Cloudflare's `162.159.128.0/19`, nowhere near
  the `66.22.192.0/18` the config matches. It carries no SNI, so it cannot be
  selected by domain at all. **Zero Discord UDP flows reached sing-box** while the
  owner sat in a voice channel; the traffic egressed `direct` and died there.
- **The capture was a hardcoded literal.** `Capture{TCP: 80,443,2053,…; UDP: 443}`
  — Flowseal's own filter with the UDP half truncated, dropping
  `19294-19344,50000-50100`. A profile filtering the voice range received nothing,
  matched nothing and said nothing: the queue and the profiles were two
  independent statements about the same traffic and only one was maintained. The
  capture is now DERIVED from the composed argv, unioned onto the instance's spec
  as a floor; nft reinstalls when the port set changes and WinDivert rebuilds its
  filter. winws's rollback now derives the PREVIOUS strategy's capture rather than
  relaunching old args behind the failed one's filter.
- **`recipeRenderable` required `{{DOMAINS}}`**, which cost it the feature it was
  protecting: Flowseal's voice block — already in our catalog — was refused as
  "global" while being narrower than most domain-scoped rules. A bounded port
  range TOGETHER WITH `--filter-l7` now counts as a scope. Either alone still
  fails, and 1024-65535 is still the internet.
- **`flowseal-alt12-discord`** carries all three profiles the rule needs, which
  was impossible before recipes could hold more than one.
- Confirmed on live traffic: **3.5 MB of voice** through the desync during a real
  call, in the office, where it was not even expected to hold.

### A pinned strategy_id was a suggestion, not a pin

- `zapretExec.Enable` took the brain's resolved strategy as a parameter named `_`
  and discarded it. So `strategy_id` on a zapret chain step pinned NOTHING on the
  desktop: the composer ranked candidates by the knowledge base and ran whatever
  it preferred. Every recipe experiment run under a pin was a measurement of
  something the operator had not chosen.
- A pin is now honoured when it names a renderable candidate, and **reported when
  it cannot be** — a pin that silently does nothing turns an experiment into a
  measurement of something else. The router keeps the old path, where a chain
  `strategy_id` is honoured by switching scripts.

### The canary could escalate, then escalated too much

- The canary measured throughput and demoted recipes, but demotion only reorders
  the PICKER; whether a rule stays on the desync rung is the brain's call, and the
  brain listens to a probe that pulls a couple of hundred bytes. Two subsystems
  disagreed every ten seconds for hours and the one with evidence lost.
  `probing.Engine.SetStallOracle` — built for the router's TSPU freeze, never
  wired on the client — now carries the canary's verdict, scoped to rules still on
  a desync rung.
- **Then it over-corrected, and that was mine.** The verdict was stored and STOOD,
  failing every subsequent probe until some later canary passed — so three
  "consecutive failures" could come from one verdict, and the probe stopped being
  a second opinion. Measured: 151 probe failures in twenty minutes driving 37
  escalations, rules abandoning desync across the board. The verdict is now
  consumed by the first probe that reads it.
- The oracle returns its REASON with the verdict; the router passes its own. A
  probe failed for one cause must not be recorded with a neighbouring cause's
  explanation.

### The throughput canary was measuring the endpoint, not the path

- youtube's probe target is `generate_204` — a response with **no body**. The
  canary pulls volume from the probe target, so it read zero bytes, computed
  0 KiB/s and demoted whatever strategy was applied. Every time, for as long as
  the service has existed: the knowledge base held every zapret recipe for youtube
  at ewma 0 after ninety consecutive "failures" that measured nothing. x's target
  is robots.txt at 2932 bytes; discord's gateway returns **35**.
- Fixed at the source by telling two cases apart: a read reaching END-OF-BODY
  before the requested volume means the endpoint ran out (a fact about the URL);
  a read stopped by the deadline means the path stalled (a fact about the path,
  and the freeze this probe exists to catch). `quality.Quality.Short` carries
  which happened, and the canary refuses to judge rather than inventing a verdict.
  The frozen-path test still passes — excusing small files must not excuse
  freezes.
- Rules gained `volume_target`, a URL with real bulk used only by this probe.
- And "connects but does not carry volume" was untrue when the fetch had not
  connected: with one attempt a transport error yields zero bytes and no
  end-of-body, so it fell through to the goodput branch. The two cases now print
  differently, because a dead fetch and a crawling one point at different causes.

### Saying which engine, and when it runs something else

- The rung class was standing in for the engine, and they answer different
  questions: "zapret" does not tell you a process is carrying the rule, `nfqws`
  does — and that decides whether you go looking at recipes or at nodes.
  `NodeStatus` gained `Engine`.
- It also gained `Requested`, populated only when the brain asked for one strategy
  and the engine runs another. That divergence was silent, and it is how the pin
  defect above was eventually noticed.

### «Требуют внимания» must mean Lotsman is out of moves

- The owner sat in a working voice call while the dashboard listed Discord under
  «Требуют внимания». `broken` (chain exhausted, the operator must act) and
  `fails > 0` (Lotsman escalating, on its own, as designed) were being painted the
  same. Alarming over the second is the crying-wolf a project with no alerting
  layer least needs.
- «Требуют внимания» is now broken-only; everything mid-escalation goes to a calm
  «Подбирает» with the failed-probe count, so a blip is distinguishable from a
  rule stuck there.


### A laptop that moved networks lost its own LAN

- Home from the office, and the ThinkPad was gone from the LAN — no ssh, no
  ping, sshd fine, internet working. `auto_route` had swallowed
  `192.168.1.0/24`, because the tun still excluded `10.0.0.0/24`.
- **The excludes were snapshotted into a reconciler that outlives the network.**
  The refresh loop holds one Reconciler for the life of the process, so every
  five minutes it regenerated the config with the subnets from wherever the
  machine booted — re-asserting the wrong LAN rather than drifting into it.
  `Reconciler.TunExcludes` recomputes them per run.
- **The recovery path claimed a success it had not observed.**
  `recaptureForNetwork` builds a fresh reconciler for exactly this, but runs
  once per confirmed roam and folded `ErrDeferred`/`ErrNotApplied` into the
  no-error branch — logging "roam: config regenerated for the new network" over
  both. A degraded subscription fetch right after a move is the ordinary case,
  and that is precisely when it declared victory. It now reports the outcome,
  and says the machine may be unreachable on its LAN until the next refresh.
- **Both verified on hardware 2026-08-07**, in the hard case the first attempt
  missed: the laptop moved between networks with DIFFERENT subnets (office
  `10.0.0.0/24` ← home `192.168.1.0/24`) thirteen hours after the service
  started and WITHOUT a restart. The excludes followed, and the periodic refresh
  two minutes later preserved them instead of re-asserting the old set — which is
  the half that was actually broken. Incidental proof: SSH into the laptop worked
  throughout, and would not have under the defect.

### Adding a subscription is no longer a dead button

- «Подписки» had a URL field and a permanently disabled «Добавить» under a note
  saying it would be wired later. Worse, the button that opened it was hidden
  when there were no subscriptions — the one moment it is the only thing you
  need.
- It now goes through the same structured config the editor uses: read the
  document, append, save. Not a second server-side path that could disagree with
  the one already there.
- The name is derived from the host so the entry is recognisable, and **tags are
  left empty on purpose** — tags decide which pools may draw on a node, and
  guessing that for somebody is how a subscription silently ends up unused. The
  form says so, and points at «Конфиг» for the rest.
- Whatever the daemon refuses comes back as its own text, shown verbatim, and the
  typed URL is kept so a rejection does not cost you the paste.

### The app says when the tunnel protects less than it looks like it does

- `/status` gained **`notices`** — conditions the operator should SEE, rendered as
  a banner above the numbers on «Обзор». Not an alerting layer, and not a
  notification: this project has neither and wants neither. The promise is that
  you learn something is broken from the device itself, and the app's job is to
  hold the CAUSE where you already look. A leak that only ever appeared in a log
  line at startup is exactly what nobody sees.
- First notice is the IPv6 escape. It is evaluated **on every status read**, not
  latched at startup, because the condition genuinely comes and goes: on the
  ThinkPad the same binary warned at 21:03 and was correctly silent at 21:39,
  IPv6 having been disabled in between. A notice cached from startup would have
  gone on accusing a machine that had already fixed itself.
- Only tunnel-intended services are named. A zapret-preferred service routes
  direct by design, so listing it would be the same claim-about-something-not-
  measured this project keeps finding.
- The text comes from the daemon verbatim; the UI keeps no copy of the wording to
  drift from the code that evaluates the condition.

### IPv6 walked past the tunnel

- `auto_route` captures the address families the tun holds an address for, and
  ours held only IPv4. So a VPN-rung service whose name resolves to AAAA
  egressed DIRECT — `ai` most of all, which had been made VPN-only that same day
  because a desync cannot help against an application-layer refusal.
- Services on a desync rung were never exposed: the nft table is `inet` and its
  rules match on interface and port, so nfqws sees v6 too. But a desync is not
  an exit, which is the whole reason `ai` was moved.
- `-tun-ipv6` captures it, **off by default**. Capturing IPv6 that the exit
  nodes cannot carry trades a silent leak for dead connections, and which is
  worse depends on the fleet — that is the operator's call. Not being told is
  not, so the client now warns at startup when it is about to protect IPv4 only
  on a host with a global IPv6 address, and names the services affected.

### 44 of 50 exits were being thrown away

- **A node's identity hashed protocol|address|port and nothing else.** The
  owner's provider sells 50 named cities that live behind 6 addresses and are
  told apart by REALITY shortId — so 50 nodes collapsed to 6, and the other 44
  were dropped as duplicates. Silently, and for as long as that provider has
  been in the config.
- It was not a cosmetic undercount. Three entries on one address, differing only
  by shortId, were dialled and asked what the internet saw: `198.51.100.10`,
  `198.51.100.11`, `198.51.100.12` — the Netherlands, Denmark and Germany. The
  provider picks the exit BY the field the identity ignored, so what was being
  discarded was exit diversity, the thing the whole fleet exists to have.
- The naive fix breaks LOT-1: another provider ROTATES shortId on the same node
  every fetch, and the reconciler deliberately ignores that so a re-fetch is not
  a fleet-wide restart. The repo's own test for it failed, which is what stopped
  the naive fix from shipping.
- Told apart by the SHAPE of one fetch instead. A rotating credential appears
  once per address; a selector appears many times at once. So the identity widens
  only for addresses carrying several nodes in the same snapshot — 50 stay 50,
  and a rotating single node stays one node through any number of rotations.
- Measured on the ThinkPad against the live config: **9 nodes → 53**,
  `vpn_url_test` 52 members, `vpn_url_test_udp` 3.

### The Ноды tab, and honest counting

- The tab showed one number for a fleet the operator could not see. It now shows
  what each entity actually is: nodes fetched, nodes in each pool, which are
  warm, and every node grouped by the subscription it came from — because "6" and
  "50" and "52" are four different questions and were all being answered with one
  figure.
- An empty pool a chain references is called out rather than rendered as a zero,
  since that is the shape of a rung with no executor behind it.
- The top bar shows the BUILD instead of the verdict. The verdict is already on
  its own tile, and the same number in two places is one place to disagree with
  itself; which build is answering had nowhere to appear at all.
  `control.Report` gained `Version` — the DTO never carried it.

### Reload could not apply a config that added a rule-set

- `Core.generate` provisions the `.srs` a config names, and Start goes through
  it — but the RECONCILER builds its own sing-box config and never did. Every
  path that reconciles rather than starts (Reload, the refresh tick, the roam
  regeneration) validated a config naming rule-sets the disk did not have, failed
  its own `sing-box check`, and fell back to a full re-exec.
- The fallback worked, which is why nobody noticed: the config landed, the box
  came up. But a re-exec drops the tunnel, and avoiding exactly that is what
  in-place reload is for. Adding a rule-set from the config editor paid that cost
  every time.
- `reconcileBox` provisions first at all three sites; provisioning a warm cache
  is a no-op, so the ordering stops being something to remember.

### The live harness grew a client mode

- `test/live/run.sh --client` covers the desktop client: config edit and reload,
  the service switch, a network change swapping the knowledge base, and a real
  suspend/resume driven by `rtcwake`.
- Suspend/resume and a live roam had never once been tested. Both passed: the
  client survives the suspend and resumes probing **600ms** after wake.
- Running it found two harness lies before it found anything about the client.
  C6 judged survival by grepping the log for a shutdown line that its own
  teardown always writes, and reported a live client as dead; it now asks the
  kernel whether the process is there, before killing it. C8 asserted no
  `lotsman-client` was running, which stopped being true the moment the client
  became a permanent service — it now distinguishes the service's own processes
  from the run's by executable path.

### Domain packs you can write yourself

- **`domains:` on a hostlist.** A pack could only ever be assembled from remote
  URLs, so declaring "the four hosts my bank uses" as a reusable list meant
  publishing it somewhere and fetching it back. Now a pack may carry its own
  entries, its sources, or both, and one with neither is refused by name.
- They come from the DECLARATION, not the file, so they work on a fresh install
  before the rebuild job has run — and for a sources-less pack, where a file may
  never exist at all.
- They merge AFTER exclusion, deliberately: an upstream exclude list quietly
  deleting a domain somebody typed by hand would be invisible and maddening, and
  what you wrote yourself outranks a third party's opinion. They go through the
  same parser as fetched entries, so a hand-typed `YouTube.com` is lowercased and
  a hand-typed `not a domain` is refused at config parse.
- «Списки» in the app gained the field, and the round-trip test locks the
  PascalCase key the form binds — a rename would otherwise still save the config
  and silently drop what the operator typed.

### A running service that reported itself missing

- **`control.DefaultSocketPath` read "permission denied" as "not there".** The
  system socket's directory is `0750 root:lotsman`, so a process outside the group
  gets EACCES from the stat, not ENOENT — and the `err == nil` check fell straight
  through to a per-user path that was never going to exist. The GUI then said `no
  such file or directory` about it while the service was running perfectly. Seen
  the day the systemd unit was installed: a desktop session started before the
  group existed cannot traverse the directory. A path we are merely forbidden to
  look at is evidence the service IS there, and is now chosen on that basis.
- A failed dial now says what it means. "Permission denied" and "no such file"
  both render in a UI as "the service is broken", and one of them is usually
  wrong; the client names the likely cause and the remedy instead.
- **And the obvious remedy is the wrong one.** "Log out and back in" is what a
  group change normally calls for, and on a systemd desktop it does nothing:
  `systemd --user` is per USER, not per session, so it survives the logout with
  the group set it started with, and everything the app launcher starts inherits
  that. Measured on the ThinkPad — a full logout left the manager's groups
  unchanged. `loginctl terminate-user` or a reboot is what refreshes them, and
  that is what the message and the NixOS module now say.

### A desync engine for Windows

- **`client/platform/winws`** runs zapret's Windows engine as a managed child,
  with the same keep-last-good discipline as the Linux one: the previous argv
  survives a failed launch, and a strategy the engine refuses leaves the previous
  one running and says so.
- The Windows gap was narrower than "no code". Both modules already cross-compiled
  for Windows, and `pkg/strategyimport` already reads Flowseal's `.bat` bundles —
  it drops `--wf-tcp`/`--wf-udp` from recipes precisely so the Windows and Linux
  editions of a strategy are one catalog entry. So the whole 120-recipe pool and
  everything the KB has learned carry over untouched; what was missing was an
  executor to hand them to.
- `CaptureArgs` turns the capture spec into WinDivert filter flags. The spellings
  come from a real bundle in the test fixtures, not from memory. An empty capture
  is an error: winws with no filter starts, reports itself healthy and desyncs
  nothing.
- `Core.newZapretExec` now picks the engine by platform in one place. `-nfqws-bin`
  defaults to empty so each engine resolves its own binary — hardcoding `nfqws`
  sent Windows looking for a binary that does not exist there.
- **Unproven, and it should be read that way:** no part of this has run on
  Windows. Process management, argv assembly and rollback are unit-tested; "winws
  starts and desyncs traffic" is not tested at all. The foreign-tunnel guard is
  worse than unproven there — it matches Linux adapter names, so on Windows it is
  very likely blind, and the engine now says so out loud rather than letting a
  safety check quietly stop checking.

### Services can be switched off

- **`disabled: true` on a service** takes it out of `buildRegistry` entirely, so
  nothing probes, routes or desyncs it. The alternative — a flag every consumer
  checks — makes one forgotten guard into a service that reads as off and is still
  probed; there is no such guard to forget when the service is simply not there.
  The declaration stays in the file, because "I don't use Discord" is not "delete
  my Discord config". A config whose services are all disabled is rejected by name
  rather than starting with nothing to steer.
- **`POST /service/{name}/enabled`** is the control-API switch, and it edits the
  YAML node in place (`config.SetServiceEnabledYAML`) rather than re-serialising
  the document. A structured save may legitimately rewrite the file; a toggle on
  the dashboard must not expand every zero value the operator never wrote or drop
  their comments. Switching back on removes the key, restoring the text.
- Validation runs over the whole candidate before the file is touched, so a
  rejected switch leaves the config intact. Applied through `Core.Reload`, in
  place — though sing-box does restart when a service leaves or rejoins, because
  its route rules genuinely changed.
- `GET /status` gained `disabled`, and the dashboard a **Выключены** section: a
  service absent on purpose and a service that has gone missing look identical
  otherwise.
- Verified on the ThinkPad in proxy mode: off → absent from `services`, present in
  `disabled`, comments intact in the file, back on → the original text; unknown
  service, missing field and last-service-off all refused with the file unchanged.

## [v7.0] — 2026-08-05

**The box can find its own strategies.** v6 could only use recipes somebody else
wrote; v7 searches for them, proves them against live DPI, and keeps what holds.

Deployed to the R5S the same day, replacing v6.12 (which had been running since
19 June). Backups of v6.12 live on the router, the ThinkPad and the Mac.

### The tuner arm, wired end to end

- **`zapret.Sandbox`** measures a candidate strategy on its own NFQUEUE while the
  household keeps running on the incumbent. Isolation is a firewall mark: the
  sandbox table queues only marked egress to the candidate engine, and the
  production table (`GenerateNft`) now emits `meta mark 0x4554 return` ahead of
  its queue rules so a marked probe is desynced exactly once. Both tables sit on
  the same hook and both run — without that skip rule the probe would be desynced
  twice and describe neither strategy.
- **`desynctune.TwoPhase`** prescreens cheaply over every candidate and spends
  volume only on the survivors. One pass cannot do both jobs: loss alone crowns a
  recipe that connects and then crawls, throughput on all of them is tens of
  minutes of uplink. Trials are strictly serial — a correctness requirement, since
  parallel handshakes to one SNI provoke the freeze being measured.
- **`desynctune.Gate`** admits one pass at a time (the sandbox is a singleton),
  with a per-service cooldown and global spacing, stamped on release regardless of
  outcome — a search that keeps failing is the worst case to hurry.
- **`pkg/prospect`** accumulates what this box proved for itself. A win is a LEAD;
  only a second win in a separate pass promotes it into the composer's pool, and
  repeated losses drop it out. Leads are re-tested before anything new is
  searched, because a discovery pass generates candidates fresh and a lead nobody
  offers again never earns its second win.
- The search **seeds from what has held** rather than a cold grid, and a
  **correlated collapse** of proven strategies — gated on the link being otherwise
  healthy — is read as the DPI having moved, which invalidates stale negative
  evidence. The gate is the whole safety: a dead uplink fails everything at once
  too, and a detector that could not tell them apart would erase months of
  evidence during a five-minute outage.
- Orphans are reclaimed at startup from the kernel's NFQUEUE table, since an
  unclean stop leaves both a table and an engine, and procd restarts this daemon.

### The bundle updater, which had stopped the engine for a month

The R5S ran without `nfqws` for a month. The cause was not Flowseal: our own
auto-updater had no flag, armed whenever `-check-interval` was positive, and
repointed the bundle symlink with no validation. A bundle carries the release's
artifact AND our state — the `-user` exclude lists nobody ships but the strategy
script requires — so a stateless extract dropped them and `flags bypass` made the
loss silent.

- State is carried forward into the new bundle; a canary runs the production argv
  against the candidate on a spare queue and keeps the working release in service
  if it fails; `-flowseal-update` can pin the bundle.
- **`pkg/strategyimport`** reads third-party bundles instead of transcribing them
  by hand, so an update ADDS recipes rather than only risking the engine. One
  normalizer over `.bat`, shell and markdown, since winws and nfqws share the flag
  family. IDs derive from the normalized arguments so the KB keeps its history. On
  the router: 67 curated + 79 read = 120 in the pool.

### Everything else measured rather than assumed

- A strategy the engine refuses **rolls back** instead of leaving it dead — on the
  router verified against the live queue, on the client by keeping the previous
  argv across the attempt.
- The canary judges on **throughput**, not reachability: a path that connects and
  then stalls is TSPU's signature failure, and the KB was being taught to prefer
  frozen paths.
- **Node ranking measures what a node carries.** A node under the volume freeze
  answers a delay test in 40ms and carries nothing, so an RTT-only ranking crowned
  the deadest exit in the pool.
- Burst probes never run while a live UDP session is on the link, decided by
  sampling the connection table twice and comparing rates — the first version
  asked only whether a UDP flow existed, which an NTP exchange satisfies, and
  would have deferred forever.
- **Rule-set updates roll back by snapshot**, spanning the swap AND the reconcile
  that validates it — the piece `docs/AUTOUPDATE.md` specified in June and the code
  never had.
- `nfqws`'s last words are recorded when it dies on its own, instead of a bare
  exit status.
- **One version, one build, every target** (`pkg/version`, `scripts/build.sh`).
  There were no tags before this, and `cmd/lotsmand` held a `version = "dev"` that
  nothing set.
- **`test/live/run.sh`** runs the guarantees on the box itself. Its first run
  found four ways the harness lied and zero defects in the code.

### Found immediately after deploying, not yet fixed

- **The EMERGENCY rung is unreachable.** Four services have two-step chains ending
  in EMERGENCY and there is no executor for that class; `social` escalated into it
  and got `no executor for class`. `emergency_pool` is empty besides. Being
  removed from the config rather than implemented — there is no always-working
  exit to put behind it.
- **CI fails on every push** with a 0s run and no jobs: a workflow-file problem
  rather than a test failure.
- **Rule-sets are still updated by the old cron**, which has no shrink guard, no
  validation and no rollback — the three absences that caused the flowseal
  outage. `pkg/rulesets` has all three and is simply not armed.

### Known limits

The generator's 48 candidates are simpler than the recipes that actually work
here — the first live pass returned a truthful "no recipe beats baseline" against
a blocked service. Seeding the search from the 120-recipe catalog rather than
eight hand-written seeds is the next piece. The busy check is evaluated at the
start of a pass, not during it. And the desktop client's suspend/resume has still
never been tested.

## v7.0 in detail — branch `prerelease-fixes`

The full record behind the summary above: the desktop-client track and the
pre-release engine hardening that preceded it. 137 commits ahead of `main`.
Everything below is validated live — on a NixOS ThinkPad (sing-box 1.13.14) for the client
work, on the R5S for the router work — unless noted. See `docs/DESIGN-client-ui.md` and
`docs/DESIGN-dns-and-tun.md` for design + handoff.

### The bundle updater, and the month the router had no desync engine

The R5S was found running without `nfqws` for a month. The cause was not Flowseal: it was
our own auto-updater, which had no flag, armed itself whenever `-check-interval` was
positive, and repointed `flowseal-current` at a freshly unzipped release with no validation
of any kind. A bundle carries the release's artifact **and our state** — the `-user` exclude
lists nobody ships but the strategy script requires — so a stateless extract dropped them,
`active.sh` refused to start, and nft's `flags bypass` made the loss silent: traffic kept
flowing, undesynced. The same shape hit on 19 June and 4 July. Every time it was us.

- **State is carried forward.** `Install` copies into the new bundle any file the release
  does not ship in a preserved subdirectory (`lists/` by default), never overwriting one it
  does. The `-user` files were still present in `flowseal-1.9.9a`/`1.9.9b` and absent from
  `1.9.9c` on, so the loss is dated to that update — and this alone would have prevented it.
- **A canary decides whether a bundle becomes the one in service.** It lifts the argv out of
  the strategy script production actually runs, repoints every bundle path at the candidate,
  moves the engine onto a queue no nft rule diverts to, and launches it; nfqws validates its
  inputs *after* startup, so survival is the signal and a failure carries the engine's own
  words. It is an A/B — the same argv runs against the current bundle first, because a
  single shot cannot tell "this bundle is broken" from "the engine cannot start here at
  all", and a canary that confused those would freeze updates forever. No baseline, no
  verdict.
- **`-flowseal-update`** (default true, today's behaviour) so the bundle can be pinned, and
  an actual swap now logs at WARN.
- Found by running the canary on the box: production reaches the engine through a shell,
  which removes the quoting, while we exec directly — so quotes travelled into argv and
  every path was wrong, failing the baseline too. It would have stood aside every time.

### Reading other people's strategies (`pkg/strategyimport`)

The catalog was already generated — by `gen.py`, whose strategies a person transcribed by
hand, so it froze at Flowseal 1.9.9a while the box moved to 1.10.0. An update could only
ever cost us and never pay, because nothing read what it brought.

- One normalizer over three containers (Flowseal `.bat` with caret continuations, shell with
  variables, fenced markdown), since winws and nfqws share the flag family — which also makes
  the `.bat` reader most of what a Windows engine will need. Deployment detail dropped,
  payload paths reduced to the basename nfqws resolves against its working dir, host
  selectors collapsed to `{{DOMAINS}}`, `--ipset` templated as the curated catalog already
  spells it. Nothing is ever executed.
- **IDs derive from the normalized arguments alone**, never the source or its order, because
  the KB keys outcomes on the id — one that moved on a bundle update would silently discard
  everything learned about that strategy.
- **A whitelist, not a blacklist**: an unmodelled flag skips the recipe naming the flag. The
  alternative is worse both ways — nfqws rejecting it kills the engine and every other
  service's block with it, and nfqws accepting it means scoring a strategy we cannot
  describe.
- **Wired to the updater**: the daemon reads the installed bundle at startup and after every
  update, folding it into the composer's pool via `zapretgen.SetRecipes`. Additive (the
  curated catalog stays whole, so an unreadable bundle costs nothing) and base-preferring (a
  recipe in both keeps the curated id). Live on the box: 67 curated + 79 read = 53 added.
- Validated against ground truth, not itself: importing the router's own scripts reproduces
  all seven of the recipes a person derived from them **byte for byte**, including the one
  that beat live TSPU. Running it on the router also found that `ImportDir` returned nothing
  there while reading 79 locally — callers hold the stable *symlink*, and `WalkDir` does not
  follow one, reporting no error and no files.

### Smaller, same theme

- **nfqws's last words are no longer thrown away.** `Apply` already teed its output into a
  4KiB tail buffer, but only read it on the immediate-exit path; an engine that died later —
  the case that happens in the field — was reported as a bare exit status. The reaper now
  carries the tail and the argv that provoked it, appends a timestamped record to
  `nfqws-crash.log`, and distinguishes an exit we asked for from one the engine chose.
- **Documentation audit**: 114 agents over every doc, each finding independently refuted
  before being applied. Claims of *absence* rot fastest — "not built yet", "unexercised",
  "no tray yet". `RELEASE-CHECKLIST.draft.md` took 14 corrections, the most of any file and
  the one that defines what "done" means; `SOURCES.md` still called the project MIT-intended
  after the GPLv3 relicense.

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
