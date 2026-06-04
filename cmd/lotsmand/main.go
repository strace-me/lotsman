// Command lotsmand is the M0 walking skeleton: one in-process daemon wiring
// all four components (Brain, Applier, KB, prober) over a channel bus. It
// proves the control loops end-to-end — init -> escalate -> recover — for one
// service (youtube).
//
// Flags:
//
//	-dry-run    executors log instead of touching nft/clash (default true)
//	-simulate   use a scripted prober (timeline) instead of real HTTP probes
//	-interval   probe period
//	-duration   run for this long then exit (0 = until SIGINT)
//
// Demo (no hardware):  lotsmand -simulate -interval=500ms -duration=28s
// Hardware (R5S):      lotsmand -dry-run=false -interval=5m -clash-base=http://127.0.0.1:9090
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/strace-me/lotsman/pkg/adaptive"
	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/anomaly"
	"github.com/strace-me/lotsman/pkg/applier"
	"github.com/strace-me/lotsman/pkg/audit"
	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/correlate"
	"github.com/strace-me/lotsman/pkg/damper"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/executor"
	"github.com/strace-me/lotsman/pkg/faillog"
	"github.com/strace-me/lotsman/pkg/flowseal"
	"github.com/strace-me/lotsman/pkg/incident"
	"github.com/strace-me/lotsman/pkg/iplearn"
	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/metrics"
	"github.com/strace-me/lotsman/pkg/misroute"
	"github.com/strace-me/lotsman/pkg/noderank"
	"github.com/strace-me/lotsman/pkg/observe"
	"github.com/strace-me/lotsman/pkg/periodic"
	"github.com/strace-me/lotsman/pkg/probing"
	"github.com/strace-me/lotsman/pkg/reconcile"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/remctl"
	"github.com/strace-me/lotsman/pkg/remediate"
	"github.com/strace-me/lotsman/pkg/rulesets"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/state"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/tspu"
	"github.com/strace-me/lotsman/pkg/vpnbalance"
)

func main() {
	var (
		configPath      = flag.String("config", "", "path to YAML config (registry+subscriptions+pools); empty = builtin youtube only")
		dryRun          = flag.Bool("dry-run", true, "log data-plane actions instead of executing them")
		simulate        = flag.Bool("simulate", false, "use a scripted prober timeline instead of real HTTP probes")
		interval        = flag.Duration("interval", 5*time.Minute, "probe period")
		duration        = flag.Duration("duration", 0, "run for this long then exit (0 = until SIGINT)")
		clashBase       = flag.String("clash-base", "http://127.0.0.1:9090", "sing-box clash-api base URL")
		probeProxy      = flag.String("probe-proxy", "", "SOCKS5 host:port (sing-box socks inbound) to route probes through, so box probes follow the LAN path; empty = probe direct")
		vpnSelector     = flag.String("vpn-selector", "", "Clash selector tag to actively rebalance across its concrete nodes (empty = off); needs -check-interval and a selector outbound in sing-box")
		vpnProbeURL     = flag.String("vpn-probe-url", "https://www.gstatic.com/generate_204", "URL for per-node Clash /delay health checks used by -vpn-selector")
		nodeRank        = flag.Bool("noderank", false, "per-service node ranker: probe each VPN-pool node THROUGH the service's own URL and pin sel-<svc> to the best (country-narrowed); off = pools ride plain url-test")
		clashSecret     = flag.String("clash-secret", "", "clash-api secret")
		zapretDir       = flag.String("zapret-script-dir", "/opt/zapret-lotsman", "dir holding <strategy>.sh nfqws launchers")
		zapretActive    = flag.String("zapret-active-link", "/opt/zapret-lotsman/active.sh", "symlink the nfqws init runs (Lotsman repoints it)")
		zapretInit      = flag.String("zapret-init", "/etc/init.d/nfqws", "nfqws init script to restart on strategy switch")
		byedpiDir       = flag.String("byedpi-script-dir", "/opt/byedpi-lotsman", "dir holding <strategy>.sh byedpi launchers")
		byedpiActive    = flag.String("byedpi-active-link", "/opt/byedpi-lotsman/active.sh", "symlink the byedpi init runs")
		byedpiInit      = flag.String("byedpi-init", "/etc/init.d/byedpi", "byedpi init script to restart on strategy switch")
		auditLog        = flag.String("audit-log", "", "append state transitions as JSONL to this path (empty = disabled)")
		failLog         = flag.String("fail-log", "", "append individual probe failures as JSONL to this path for later review (empty = disabled)")
		metricsAddr     = flag.String("metrics-addr", "", "expose Prometheus /metrics on this addr, e.g. 127.0.0.1:9101 (empty = disabled)")
		stateFile       = flag.String("state-file", "", "persist/restore chain positions to this JSON file (empty = disabled)")
		kbFile          = flag.String("kb-file", "", "persist/restore learned strategy success (KB) to this JSON file so experience survives restart (empty = disabled)")
		smart           = flag.Bool("smart", true, "enable the intelligence layer (policy/correlate/damper/adaptive/anomaly) in escalation decisions")
		checkInterval   = flag.Duration("check-interval", 0, "run background maintenance (Flowseal update, subscription refresh) every interval (0 = disabled)")
		observeInterval = flag.Duration("observe-interval", 30*time.Second, "run the passive-observation eye (observe/detect/propose, PROPOSE-ONLY) every interval, independent of -check-interval (0 = disabled)")
		remediateArm    = flag.Bool("remediate", false, "ARM the self-heal remediation ladder (LOT-18b): the observe loop AUTO-APPLIES remediations to the live sing-box config with canary+auto-rollback. DEFAULT OFF = propose-only. Requires -reconcile + -singbox-config; refuses to arm otherwise. -dry-run still gates whether reconcile actually writes.")
		incidentLog     = flag.String("incident-log", "", "append armed-remediation lifecycle events (detected/applied/resolved/rolled-back/escalated) as JSONL to this path (empty = disabled)")
		flowsealBase    = flag.String("flowseal-base", "/opt", "parent dir for Flowseal bundles (holds flowseal-current symlink)")
		reconcileSB     = flag.Bool("reconcile", false, "daemon owns the sing-box config: regenerate from config+subs and apply on structural change (needs -singbox-config + a config with subscriptions; -dry-run gates whether it actually applies)")
		singboxConfig   = flag.String("singbox-config", "", "path to the sing-box config the daemon reconciles/owns")
		singboxBin      = flag.String("singbox-bin", "sing-box", "sing-box binary used for `check`")
		singboxRestart  = flag.String("singbox-restart", "/etc/init.d/sing-box restart", "command to restart sing-box (space-separated)")
		reconcileBackup = flag.String("reconcile-backup-dir", "", "dir for pre-apply sing-box config backups (empty = no backup)")
		reconcileBase   = flag.String("reconcile-baseline-file", "", "persist the reconcile anti-churn node-count baseline to this JSON file so the degraded-fetch guard survives restart (empty = in-memory only)")
		rulesetsUpdate  = flag.Bool("rulesets-update", false, "Track A autoupdate: fetch the rule-set release, shrink-guard, swap changed .srs, trigger reconcile")
		rulesetsRepo    = flag.String("rulesets-repo", "runetfreedom/russia-v2ray-rules-dat", "GitHub repo whose release ships sing-box.zip (.srs bundle)")
		rulesetsPin     = flag.String("rulesets-pin", "", "pin a release tag (empty = track latest)")
		rulesetsBump    = flag.Bool("rulesets-autobump", true, "evaluate latest even when pinned; move the pin only if it validates")
		rulesetsRatio   = flag.Float64("rulesets-min-ratio", 0.7, "shrink guard: reject a tag dropping below this fraction of its last good count (0 = off)")
		rulesetsDir     = flag.String("rulesets-dir", "/etc/sing-box", "rule-set root holding rule-set-{geosite,geoip}/*.srs")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("lotsmand starting", "dry_run", *dryRun, "simulate", *simulate, "interval", interval.String())

	bus := events.NewBus()
	knowledge := kb.New()

	// Registry comes from config when given, else the builtin youtube service.
	// With a config we also exercise the subscription->pool pipeline at startup
	// and log membership counts (the nodes do not yet feed sing-box generation).
	reg := registry.Builtin()
	var conf *config.Config
	if *configPath != "" {
		loaded, err := config.Load(*configPath)
		if err != nil {
			log.Error("config load failed", "path", *configPath, "err", err)
			os.Exit(1)
		}
		conf = loaded
		reg = conf.Registry
		services := make([]registry.Service, 0, len(reg.Services))
		for _, s := range reg.Services {
			services = append(services, s)
		}
		for _, c := range registry.DetectConflicts(services) {
			log.Warn("service match overlap (route order decides which wins)",
				"kind", c.Kind, "value", c.Value, "services", c.Services)
		}
		loadPools(context.Background(), conf, log)
	}

	// Strategy catalog: builtin launchers + any declared in config. It bounds
	// which zapret strategies the KB may propose, so an empty ALT_ZAPRET step
	// never resolves to a strategy with no launcher on disk.
	catalog := strategy.BuiltinCatalog()
	if conf != nil {
		for _, d := range conf.Strategies {
			catalog.Add(d)
		}
	}
	knowledge.SetZapretSeed(catalog.ZapretIDs())

	// Restore learned strategy success so a restart resumes ranking from
	// experience instead of cold-starting at the seed. Best-effort: a missing
	// file is a fresh start, a corrupt one only warns.
	if *kbFile != "" {
		if err := knowledge.Load(*kbFile); err != nil {
			log.Warn("kb load failed (starting from seed)", "path", *kbFile, "err", err)
		} else if _, statErr := os.Stat(*kbFile); statErr == nil {
			log.Info("kb restored", "path", *kbFile)
		} else {
			log.Info("kb cold start (no prior file)", "path", *kbFile)
		}
	}

	// Going live: every zapret strategy we could symlink as active must have its
	// launcher on disk, else the symlink dangles and nfqws restarts into a broken
	// config (an outage). Fail fast instead. In dry-run/simulate nothing is
	// symlinked, so missing scripts are harmless and we only warn.
	applyable := zapretStrategyIDs(reg, catalog)
	if missing := strategy.MissingScripts(applyable, *zapretDir); len(missing) > 0 {
		if *dryRun || *simulate {
			log.Warn("zapret strategies have no launcher script (harmless in dry-run/simulate)",
				"missing", strings.Join(missing, ","), "dir", *zapretDir)
		} else {
			log.Error("zapret strategies have no launcher script; refusing to run (would dangle the active symlink)",
				"missing", strings.Join(missing, ","), "dir", *zapretDir)
			os.Exit(1)
		}
	}

	// Executors. VPN/Direct switch the sing-box selector via Clash API (all
	// tproxy traffic is already in sing-box on the R5S). Zapret switches the
	// active global nfqws strategy (symlink swap + service restart).
	clash := dataplane.NewClashClient(*clashBase, *clashSecret)

	// The node ranker is ADVISORY: it probes nodes and records a best-node
	// recommendation per service. Brain's applier is the SINGLE writer of the
	// data plane and reads this recommendation only when it applies a VPN step,
	// so the ranker can never override a service Brain placed on zapret/direct.
	var ranker *noderank.Ranker
	var bestNode func(string) string
	if *nodeRank && conf != nil {
		ranker = noderank.New(clash, 3, *dryRun, log)
		bestNode = ranker.Best
	}

	zapretEx := executor.NewZapret(executor.ExecRunner{}, *zapretDir, *zapretActive, *zapretInit, *dryRun, log)
	byedpiEx := executor.NewByeDPI(executor.ExecRunner{}, *byedpiDir, *byedpiActive, *byedpiInit, *dryRun, log)
	// When the daemon owns a per-service config (reconcile), a zapret/byedpi step
	// must also route the service direct -> nfqws via its sel-<service> selector.
	// Without reconcile the box runs the hand-tuned single-selector config where
	// those selectors do not exist, so leave routing to the global engine.
	if *reconcileSB {
		zapretEx.RouteToDirect(clash)
		byedpiEx.RouteToDirect(clash)
	}
	execs := []executor.StrategyExecutor{
		zapretEx,
		byedpiEx,
		executor.NewVPN(clash, *dryRun, log).WithBestNode(bestNode),
		executor.NewDirect(clash, *dryRun, log),
	}

	// Brain config: spec defaults, shortened in simulate mode so the full
	// escalate->recover loop is observable in seconds.
	cfg := brain.DefaultConfig()
	if *simulate {
		cfg = brain.Config{EscalateFails: 3, RecoverSuccess: 5, SettlingWindow: 2 * time.Second}
	}

	var rec audit.Recorder = audit.Nop{}
	if *auditLog != "" {
		fr, err := audit.NewFileRecorder(*auditLog, log)
		if err != nil {
			log.Error("audit log open failed", "path", *auditLog, "err", err)
			os.Exit(1)
		}
		defer fr.Close()
		rec = fr
		log.Info("audit log enabled", "path", *auditLog)
	}

	var fails faillog.Recorder = faillog.Nop{}
	if *failLog != "" {
		ff, err := faillog.NewFileRecorder(*failLog)
		if err != nil {
			log.Error("fail log open failed", "path", *failLog, "err", err)
			os.Exit(1)
		}
		defer ff.Close()
		fails = ff
		log.Info("fail log enabled", "path", *failLog)
	}

	// Incident log (LOT-18b): the armed remediation controller's lifecycle trail.
	var incidents incident.Recorder = incident.Nop{}
	if *incidentLog != "" {
		ir, err := incident.NewFileRecorder(*incidentLog, log)
		if err != nil {
			log.Error("incident log open failed", "path", *incidentLog, "err", err)
			os.Exit(1)
		}
		defer ir.Close()
		incidents = ir
		log.Info("incident log enabled", "path", *incidentLog)
	}

	br := brain.New(bus, reg, knowledge, cfg, rec, log)
	// Re-converge the data plane toward desired state every probe interval, so a
	// sing-box restart that reset selectors — or fresh node-ranker advice — heals
	// without waiting for a state transition. Single writer, idempotent.
	br.SetReassert(*interval)
	ap := applier.New(bus, execs, log)

	if *stateFile != "" {
		store := state.NewFileStore(*stateFile)
		br.Restore(store.Load())
		br.SetPersist(func(pos map[string]int) { store.Save(pos) })
		log.Info("state persistence enabled", "path", *stateFile)
	}

	// Intelligence layer: compose correlate/damper/adaptive/anomaly into Brain's
	// escalation decision via policy. Without it, Brain uses the plain threshold.
	if *smart {
		corr := correlate.New(2, 0.6) // >=2 services and >=60% down => systemic
		damp := damper.New(10*time.Minute, 3, 30*time.Second, 10*time.Minute)
		tuner := adaptive.NewTuner(adaptive.Thresholds{EscalateAt: cfg.EscalateFails, RecoverAt: cfg.RecoverSuccess})
		anomalies := map[string]*anomaly.Detector{}
		for name := range reg.Services {
			anomalies[name] = anomaly.New(anomaly.DefaultConfig())
		}
		br.SetSmarts(&brain.Smarts{
			FeedAnomaly: func(s string, ok bool, rtt int) {
				if d := anomalies[s]; d != nil {
					d.Observe(ok, rtt)
				}
			},
			ObserveHealth: func(s string, healthy bool) { corr.Set(s, healthy) },
			Threshold: func(s, activeStrategy string) int {
				rel := knowledge.Stats(s, activeStrategy).Success
				return tuner.Tune(rel, damp.Count(s, time.Now())).EscalateAt
			},
			Systemic:    corr.Systemic,
			FlapBackoff: damp.Backoff,
			Anomaly: func(s string) anomaly.State {
				if d := anomalies[s]; d != nil {
					return d.State()
				}
				return anomaly.Healthy
			},
			RecordSwitch: damp.Record,
			SuggestClass: func(errText string, rttMs int) string {
				return tspu.SuggestClass(tspu.Classify(tspu.Signals{OK: false, Err: errText, RTTms: rttMs}))
			},
		})
		log.Info("intelligence layer enabled", "components", "policy/correlate/damper/adaptive/anomaly")
	}

	mc := metrics.New(br.Snapshot, knowledge.Snapshot)
	if *metricsAddr != "" {
		srv := mc.Serve(*metricsAddr)
		defer srv.Close()
		log.Info("metrics enabled", "addr", *metricsAddr, "path", "/metrics")
	}

	var prober dataplane.Prober
	if *simulate {
		prober = dataplane.NewFuncProber(demoTimeline)
	} else {
		specs := map[string]dataplane.ServiceProbe{}
		for name, svc := range reg.Services {
			specs[name] = dataplane.ServiceProbe{Type: svc.ProbeType, Target: svc.ProbeTarget}
		}
		prober = dataplane.NewMultiProberProxy(specs, *probeProxy)
		if *probeProxy != "" {
			log.Info("probing through sing-box socks inbound", "proxy", *probeProxy)
		}
	}
	eng := probing.New(bus, prober, br, reg, knowledge, mc, fails, *interval, log)

	// The sing-box config reconciler is constructed once here (independent of the
	// maintenance loop) so both the maintenance loop AND the armed remediation
	// controller (LOT-18b) can share the same instance — the controller needs it
	// to apply/revert remediations even when -check-interval is off.
	var rc *reconcile.Reconciler
	if *reconcileSB && *singboxConfig != "" && conf != nil {
		rc = newReconciler(conf, reg, clash, *singboxConfig, *singboxBin, *singboxRestart, *reconcileBackup, *reconcileBase, *probeProxy, *dryRun, log)
	}

	// Background maintenance loop (Flowseal update, subscription refresh).
	runners := []func(context.Context){br.Run, ap.Run, eng.Run}
	if *checkInterval > 0 {
		pr := periodic.New(log)
		upd := flowseal.NewUpdater(subscription.NewHTTPFetcher(), flowseal.NewFileInstaller(*flowsealBase))
		pr.Add(periodic.Task{Name: "flowseal-update", Interval: *checkInterval, Fn: func(c context.Context) error {
			out, err := upd.CheckAndUpdate(c)
			if err == nil {
				log.Info("flowseal check", "current", out.Current, "latest", out.Latest, "updated", out.Updated)
			}
			return err
		}})
		if conf != nil && len(conf.Subscriptions) > 0 {
			pr.Add(periodic.Task{Name: "subscription-refresh", Interval: *checkInterval, Fn: func(c context.Context) error {
				loadPools(c, conf, log)
				return nil
			}})
		}
		if conf != nil && len(conf.Hostlists) > 0 {
			agg := aggregate.NewManager(subscription.NewHTTPFetcher())
			for _, hl := range conf.Hostlists {
				hl := hl
				pr.Add(periodic.Task{Name: "hostlist-rebuild:" + hl.Name, Interval: *checkInterval, RunAtStart: true, Fn: func(c context.Context) error {
					return rebuildHostlist(c, agg, hl, *dryRun, log)
				}})
			}
			log.Info("hostlist rebuild enabled", "lists", len(conf.Hostlists))
		}
		if *vpnSelector != "" {
			vb := vpnbalance.New(clash, *vpnSelector, *vpnProbeURL, 3, balancer.ProfileFor("general"), *dryRun, log)
			pr.Add(periodic.Task{Name: "vpn-balance", Interval: *checkInterval, Fn: vb.Rebalance})
			log.Info("vpn node balancer enabled", "selector", *vpnSelector, "probe_url", *vpnProbeURL)
		}
		if ranker != nil {
			// RunAtStart is safe now the ranker is advisory (it records best-node
			// recommendations, never writes a selector), so it warms the advice
			// immediately after a restart instead of leaving VPN services on the
			// generic url-test pick for a whole interval. Wait for the Clash API to
			// answer first, so the very first rank (right after a sing-box restart)
			// reads real selectors instead of failing on a not-yet-up API.
			pr.Add(periodic.Task{Name: "noderank", Interval: *checkInterval, RunAtStart: true, Fn: func(c context.Context) error {
				for i := 0; i < 20; i++ {
					if _, err := clash.Proxy(c, "direct"); err == nil {
						break
					}
					select {
					case <-time.After(time.Second):
					case <-c.Done():
						return nil
					}
				}
				// rankNodes nudges Brain per service as each one's advice lands, so
				// a fast pool (e.g. the 2-node UDP pool) applies immediately instead
				// of waiting for a slow 50-node pool later in the pass.
				return rankNodes(c, conf, reg, ranker, br.ReassertService, log)
			}})
			log.Info("per-service node ranker enabled (advisory)", "dry_run", *dryRun)
		}
		if rc != nil {
			pr.Add(periodic.Task{Name: "singbox-reconcile", Interval: *checkInterval, RunAtStart: true, Fn: rc.Reconcile})
			log.Info("sing-box config reconcile enabled", "path", *singboxConfig, "dry_run", *dryRun)
		}
		if *rulesetsUpdate {
			up := newRulesetsUpdater(reg, rc, *rulesetsRepo, *rulesetsPin, *rulesetsDir, *singboxBin, *rulesetsBump, *rulesetsRatio, *dryRun, log)
			pr.Add(periodic.Task{Name: "rulesets-update", Interval: *checkInterval, Fn: up.Update})
			log.Info("rule-set autoupdate enabled", "repo", *rulesetsRepo, "pin", *rulesetsPin, "dry_run", *dryRun)
		}
		if *kbFile != "" {
			pr.Add(periodic.Task{Name: "kb-save", Interval: *checkInterval, Fn: func(context.Context) error {
				return knowledge.Save(*kbFile)
			}})
		}
		runners = append(runners, pr.Run)
		log.Info("maintenance loop enabled", "interval", checkInterval.String())
	}

	// Passive-observation eye (LOT-15/16/18a) on its own SHORT interval, separate
	// from the 15m maintenance loop (LOT-21): a ~1-minute video stall must be
	// visible to the detector, so this fires every -observe-interval and once at
	// start. It is cheap (one clash /connections GET + in-memory compute) and
	// stays PROPOSE-ONLY — it logs + publishes metrics; it never applies.
	if *observeInterval > 0 {
		op := periodic.New(log)
		eye := observe.New(clashConnSource{clash}, reg)
		var eyeMu sync.Mutex
		var eyeSnap observe.Snapshot
		mc.SetObserveSnapshot(func() observe.Snapshot {
			eyeMu.Lock()
			defer eyeMu.Unlock()
			return eyeSnap
		})
		// Misroute detector (LOT-16): turn the eye's snapshot into per-service
		// verdicts. Phase 1 is detect-only — it logs and publishes a metric, no
		// remediation, no Brain escalation (that is LOT-18).
		misrouteCfg := misroute.DefaultConfig()
		var verdicts []misroute.Verdict
		mc.SetMisrouteSnapshot(func() []misroute.Verdict {
			eyeMu.Lock()
			defer eyeMu.Unlock()
			return verdicts
		})
		// Remediation planner (LOT-18a): on each misroute verdict propose a
		// remediation rung. PROPOSE-ONLY — it logs and publishes a metric; it does
		// NOT call the generator with the plan, trigger reconcile, or touch the live
		// config (auto-apply + canary/rollback is LOT-18b). The Learner is the
		// IP-fallback CIDR source: a single instance lives for the daemon's lifetime
		// and every observe pass feeds it the matched flows' destination IPs (below),
		// so ip-fallback gets real CDN CIDRs. Cold start proposes reject-quic until
		// the first IPs land; state is in-memory and rebuilds from traffic in minutes.
		learner := iplearn.NewLearner()
		var plans []remediate.Plan
		mc.SetRemediationSnapshot(func() []remediate.Plan {
			eyeMu.Lock()
			defer eyeMu.Unlock()
			return plans
		})

		// Armed remediation controller (LOT-18b), GATED behind -remediate (default
		// OFF). When off, ctl stays nil and the loop below is unchanged propose-only.
		// When on, it REQUIRES a reconciler (needs -reconcile -singbox-config): if
		// missing, refuse to arm (log error) and stay propose-only — never act
		// without the apply mechanism. The controller owns the active-remediations
		// map; the reconciler reads it via rc.Remediations and Apply/Rollback update
		// it then call Reconcile. passCtx carries the current observe pass's context
		// into the synchronous Apply/Rollback calls.
		var ctl *remctl.Controller
		var passCtx context.Context // the current observe pass's context, used by Apply/Rollback
		if *remediateArm {
			if rc == nil {
				log.Error("-remediate set but no reconciler (needs -reconcile and -singbox-config with subscriptions); staying PROPOSE-ONLY")
			} else {
				// ActiveSet owns the per-service remediation map and keeps it
				// consistent with what reconcile actually committed (LOT-24): a failed
				// reconcile reverts the map so it never claims a state the live config
				// does not have. commit reconciles using the current observe pass's ctx.
				activeSet := remctl.NewActiveSet(func() error { return rc.Reconcile(passCtx) })
				rc.Remediations = activeSet.Snapshot
				ctl = remctl.New(remctl.DefaultConfig(), activeSet.Actions(), incidents)
				log.Warn("ARMED remediation controller enabled (LOT-18b): observe loop will AUTO-APPLY remediations with canary+rollback", "dry_run", *dryRun)
			}
		}

		op.Add(periodic.Task{Name: "observe", Interval: *observeInterval, RunAtStart: true, Fn: func(c context.Context) error {
			snap, err := eye.Observe(c)
			if err != nil {
				return err
			}
			vs := misroute.Detect(snap, misrouteCfg)
			// Feed this pass's matched destination IPs into the persistent Learner so
			// the ip-fallback rung accumulates real CDN CIDRs over time (in-memory: it
			// rebuilds from observed traffic within minutes of a restart — no file). The
			// Learner dedups/contains internally, so re-observing the same edge is cheap.
			for _, sm := range snap.Services {
				for _, ip := range sm.DestIPs {
					learner.Observe(sm.Service, ip)
				}
			}
			// Propose a remediation per misrouted service (PROPOSE-ONLY). CIDRs come
			// from the learner snapshot; nothing here applies anything.
			misrouted := make([]string, 0, len(vs))
			for _, v := range vs {
				if v.Misrouted {
					misrouted = append(misrouted, v.Service)
				}
			}
			in := remediate.InputsFromLearner(learner, misrouted)
			ps := make([]remediate.Plan, 0, len(misrouted))
			for _, v := range vs {
				if !v.Misrouted {
					continue
				}
				ps = append(ps, remediate.Decide(v, in))
			}
			eyeMu.Lock()
			eyeSnap = snap
			verdicts = vs
			plans = ps
			eyeMu.Unlock()
			for _, name := range sortedServiceNames(snap.Services) {
				sm := snap.Services[name]
				log.Info("observe", "service", name, "flows", sm.Flows,
					"leak_ratio", sm.LeakRatio, "dead_flow_ratio", sm.DeadFlowRatio,
					"udp_flows", sm.UDPFlows, "bytes", sm.Bytes)
			}
			for _, v := range vs {
				if v.Misrouted {
					log.Warn("misroute detected", "service", v.Service, "kind", v.Kind,
						"leak_ratio", v.LeakRatio, "dead_ratio", v.DeadFlowRatio, "reason", v.Reason)
				}
			}
			for _, p := range ps {
				log.Warn("remediation proposed", "service", p.Service, "rung", p.Rung,
					"action", p.Action, "cidrs", len(p.CIDRs), "reason", p.Reason)
			}
			// Armed path (LOT-18b): feed this pass's verdicts to the controller, which
			// applies/canaries/rolls-back via the reconciler. nil ctl = propose-only.
			if ctl != nil {
				passCtx = c
				ctl.Pass(vs, in)
			}
			return nil
		}})
		runners = append(runners, op.Run)
		log.Info("observe eye enabled (passive, propose-only)", "interval", observeInterval.String())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	var wg sync.WaitGroup
	for _, run := range runners {
		wg.Add(1)
		go func(r func(context.Context)) { defer wg.Done(); r(ctx) }(run)
	}
	wg.Wait()

	// Persist learned experience on a clean shutdown so the most recent EWMA
	// state (not just the last periodic snapshot) survives the restart.
	if *kbFile != "" {
		if err := knowledge.Save(*kbFile); err != nil {
			log.Warn("kb final save failed", "path", *kbFile, "err", err)
		}
	}
	log.Info("lotsmand stopped")
}

// clashConnSource adapts *dataplane.ClashClient to observe.Source: it maps the
// dataplane /connections records into the dataplane-free shape the eye consumes.
type clashConnSource struct{ c *dataplane.ClashClient }

func (s clashConnSource) Connections(ctx context.Context) ([]observe.Conn, error) {
	cs, err := s.c.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]observe.Conn, len(cs))
	for i, c := range cs {
		out[i] = observe.Conn{
			Chains: c.Chains, Upload: c.Upload, Download: c.Download,
			Host: c.Metadata.Host, DestIP: c.Metadata.DestinationIP, Network: c.Metadata.Network,
		}
	}
	return out, nil
}

// sortedServiceNames returns the snapshot's service names in stable order so the
// per-service observe log lines are deterministic.
func sortedServiceNames(m map[string]observe.ServiceMetrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// zapretStrategyIDs returns every zapret strategy that could be applied: the
// explicit StrategyIDs in service chains, plus the catalog IDs (an empty
// ALT_ZAPRET step resolves to one of these via KB.TopNZapret). Deduplicated.
func zapretStrategyIDs(reg *registry.Registry, catalog *strategy.Catalog) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, svc := range reg.Services {
		for _, step := range svc.Chain {
			if step.StrategyClass == strategy.ClassZapret {
				add(step.StrategyID)
			}
		}
	}
	for _, id := range catalog.ZapretIDs() {
		add(id)
	}
	return out
}

// rebuildHostlist fetches and merges a hostlist's sources and writes the result
// to its Out path (atomically: temp file + rename, so a crashed write never
// leaves nfqws reading a half-written list). In dry-run it logs counts without
// touching the file. A merge with zero domains is treated as a failure and does
// not overwrite a good existing list (a dead mirror must not blank the bypass).
func rebuildHostlist(ctx context.Context, agg *aggregate.Manager, hl config.Hostlist, dryRun bool, log *slog.Logger) error {
	res, errs := agg.Build(ctx, hl.Sources, hl.Exclude)
	for _, err := range errs {
		log.Warn("hostlist source issue", "list", hl.Name, "err", err)
	}
	if len(res.Domains) == 0 {
		return fmt.Errorf("hostlist %q: merged to zero domains, keeping existing file", hl.Name)
	}
	log.Info("hostlist built", "list", hl.Name, "domains", len(res.Domains),
		"sources", res.Sources, "excluded", res.Excluded, "invalid", res.Invalid, "out", hl.Out)

	// Shrink guard: if the rebuild collapsed below min_keep_ratio of the last
	// good list (e.g. an upstream half-broke), keep the existing file. A
	// deliberate protective keep-old, not an error.
	if prev := countHostlistLines(hl.Out); !aggregate.ShrinkOK(prev, len(res.Domains), hl.MinKeepRatio) {
		log.Warn("hostlist shrink guard tripped, keeping existing file", "list", hl.Name,
			"prev", prev, "new", len(res.Domains), "min_ratio", hl.MinKeepRatio)
		return nil
	}

	if dryRun {
		log.Info("dry-run: would write hostlist", "list", hl.Name, "out", hl.Out)
		return nil
	}

	body := []byte(strings.Join(res.Domains, "\n") + "\n")
	changed, err := aggregate.WriteIfChanged(hl.Out, body, 0o644)
	if err != nil {
		return fmt.Errorf("hostlist %q: write: %w", hl.Name, err)
	}
	if changed {
		log.Info("hostlist written", "list", hl.Name, "out", hl.Out, "domains", len(res.Domains))
	} else {
		log.Info("hostlist unchanged, skipped write", "list", hl.Name, "out", hl.Out)
	}
	return nil
}

// countHostlistLines counts non-blank lines in an existing hostlist file (the
// last good domain count), or 0 if the file is missing — feeds the shrink guard.
func countHostlistLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// newRulesetsUpdater builds the Track-A updater: required tags are every
// rule-set tag the services reference; on a real swap it triggers the reconciler
// (re-validate via sing-box check + apply) if one is configured.
func newRulesetsUpdater(reg *registry.Registry, rc *reconcile.Reconciler, repo, pin, dir, sbBin string, autobump bool, minRatio float64, dryRun bool, log *slog.Logger) *rulesets.Updater {
	seen := map[string]bool{}
	var required []string
	for _, s := range reg.Services {
		for _, t := range s.RuleSets {
			if !seen[t] {
				seen[t] = true
				required = append(required, t)
			}
		}
	}
	var onApplied func(context.Context) error
	if rc != nil {
		onApplied = rc.Reconcile
	}
	return &rulesets.Updater{
		Repo: repo, Required: required, LiveDir: dir, Pin: pin, AutoBump: autobump,
		MinRatio: minRatio, PerTag: true, DryRun: dryRun,
		Counter:   rulesets.SingboxCounter{Bin: sbBin},
		Releaser:  rulesets.GitHubReleaser{},
		PathFor:   singbox.RuleSetRelPath,
		OnApplied: onApplied,
		Log:       log,
	}
}

// newReconciler wires a sing-box config reconciler from the loaded config: the
// generator Options match the R5S (socks probe-in mirrors -probe-proxy), the
// Alive check waits for the Clash API to answer after a restart, and DryRun
// follows the global -dry-run so a first deployment only logs what it would do.
func newReconciler(conf *config.Config, reg *registry.Registry, clash *dataplane.ClashClient, cfgPath, sbBin, restartCmd, backupDir, baselineFile, socksProbe string, dryRun bool, log *slog.Logger) *reconcile.Reconciler {
	services := make([]registry.Service, 0, len(reg.Services))
	for _, s := range reg.Services {
		services = append(services, s)
	}
	opts := singbox.DefaultOptions()
	opts.SocksProbeListen = socksProbe
	opts.UTLSFingerprint = conf.UTLSFingerprint
	if conf.SingboxVersion != "" {
		opts.TargetVersion = conf.SingboxVersion
	}
	if conf.FakeIP != nil {
		opts.FakeIP = &singbox.FakeIPOptions{Inet4Range: conf.FakeIP.Inet4Range, Inet6Range: conf.FakeIP.Inet6Range, Resolver: conf.FakeIP.Resolver}
	}
	if conf.Multiplex != nil {
		opts.Multiplex = &singbox.MultiplexOptions{Protocol: conf.Multiplex.Protocol, MaxConnections: conf.Multiplex.MaxConnections, MinStreams: conf.Multiplex.MinStreams, Padding: conf.Multiplex.Padding, BrutalUp: conf.Multiplex.BrutalUp, BrutalDown: conf.Multiplex.BrutalDown}
	}

	// Subscription-via-VPN (LOT-28): when a via-pool is configured AND we have a
	// socks proxy to dial through, route the subscription endpoint hosts through
	// that pool (generator emits the route rule) and fetch THROUGH the tunnel so a
	// refresh egresses abroad instead of via the flaky RU direct path. Both pieces
	// are required: without the proxy the route rule alone can't catch the box's
	// own locally-generated fetch (only LAN traffic is tproxy'd), so we gate on
	// both and otherwise fetch direct exactly as before.
	loader := subscription.NewManager(subscription.NewHTTPFetcher())
	if conf.SubViaPool != "" && socksProbe != "" {
		hosts := subViaHosts(conf.Subscriptions)
		if len(hosts) > 0 {
			opts.SubViaPool = conf.SubViaPool
			opts.SubViaHosts = hosts
			loader = subscription.NewManager(subscription.NewHTTPFetcherProxy(socksProbe))
			log.Info("reconcile: fetching subscriptions via VPN tunnel", "pool", conf.SubViaPool, "hosts", strings.Join(hosts, ","), "proxy", socksProbe)
		}
	}
	alive := func(c context.Context) bool {
		for i := 0; i < 10; i++ {
			if _, err := clash.Proxy(c, "direct"); err == nil {
				return true
			}
			select {
			case <-time.After(time.Second):
			case <-c.Done():
				return false
			}
		}
		return false
	}
	rc := &reconcile.Reconciler{
		Services:   services,
		Devices:    conf.Devices,
		Opts:       opts,
		Subs:       conf.Subscriptions,
		Loader:     loader,
		Pools:      conf.Pools,
		ConfigPath: cfgPath,
		BackupDir:  backupDir,
		DryRun:     dryRun,
		Runner:     executor.ExecRunner{},
		SingboxBin: sbBin,
		RestartCmd: strings.Fields(restartCmd),
		Alive:      alive,
		Log:        log,
	}
	// Seed the anti-churn baseline from the persisted file (LOT-29) so the
	// degraded-fetch guard fires on the very first reconcile after a restart
	// instead of resetting to 0. Missing/corrupt file = cold start (Load -> 0).
	if baselineFile != "" {
		rc.Baseline = reconcile.NewBaselineStore(baselineFile)
		if n := rc.Baseline.Load(); n > 0 {
			rc.SetBaseline(n)
			log.Info("reconcile: baseline restored", "path", baselineFile, "last_nodes", n)
		} else {
			log.Info("reconcile: baseline cold start (no prior file)", "path", baselineFile)
		}
	}
	return rc
}

// subViaHosts returns the deduplicated endpoint hostnames of the FETCHED
// subscriptions (inline node share-links are excluded — they are the node, not a
// fetch). These are the hosts the generator routes through the VPN pool so a
// refresh egresses abroad (LOT-28).
func subViaHosts(subs []subscription.Declaration) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range subs {
		if !d.Enabled {
			continue
		}
		if h, ok := subscription.FetchHost(d); ok && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// loadPools fetches the configured subscriptions, computes pool membership,
// and logs counts. This exercises the parse->manager->pools pipeline end to
// end; wiring the resulting nodes into sing-box generation is a later slice.
func loadPools(ctx context.Context, cfg *config.Config, log *slog.Logger) {
	if len(cfg.Subscriptions) == 0 {
		return
	}
	mgr := subscription.NewManager(subscription.NewHTTPFetcher())
	nodes, errs := mgr.Load(ctx, cfg.Subscriptions)
	for _, err := range errs {
		log.Warn("subscription load issue", "err", err)
	}
	log.Info("subscriptions loaded", "nodes", len(nodes))
	for name, members := range cfg.Pools.Memberships(nodes) {
		log.Info("pool membership", "pool", name, "nodes", len(members))
	}
}

// rankNodes runs one per-service node-ranking cycle: for every service that has
// an HTTP probe target and a non-empty VPN pool, it ranks that pool's concrete
// nodes by probing each THROUGH the service's own URL and pins sel-<svc> to the
// winner. Candidates come from the pool membership, which is already
// country-filtered (pools' countries_exclude), so a blocked service never lands
// on a RU exit even if its ping is lowest — the failure mode of Hiddify/url-test
// this whole feature exists to fix. Per-cycle subscription reload mirrors
// loadPools; a single bad service is logged, not fatal.
func rankNodes(ctx context.Context, conf *config.Config, reg *registry.Registry, ranker *noderank.Ranker, nudge func(string), log *slog.Logger) error {
	if len(conf.Subscriptions) == 0 {
		return nil
	}
	mgr := subscription.NewManager(subscription.NewHTTPFetcher())
	nodes, errs := mgr.Load(ctx, conf.Subscriptions)
	for _, e := range errs {
		log.Warn("noderank: subscription load issue", "err", e)
	}
	memberships := conf.Pools.Memberships(nodes)
	for _, svc := range reg.Services {
		if svc.ProbeType != "" && svc.ProbeType != "http" {
			continue // Clash /delay is an HTTP GET; tcp/stun services are not rankable this way
		}
		if svc.ProbeTarget == "" || (len(svc.RuleSets) == 0 && len(svc.Domains) == 0 && len(svc.IPs) == 0) {
			continue
		}
		pool := svc.VPNPool()
		if pool == "" {
			continue
		}
		members := memberships[pool]
		cands := make([]noderank.Candidate, 0, len(members))
		for _, m := range members {
			if tag, ok := singbox.NodeTag(m); ok {
				cands = append(cands, noderank.Candidate{Tag: tag, Country: m.Country})
			}
		}
		if len(cands) == 0 {
			continue
		}
		prof := svc.Profile
		if prof == "" {
			prof = svc.Category
		}
		ns := noderank.Service{
			Name:     svc.Name,
			Selector: registry.SelectorTag(svc.Name),
			ProbeURL: svc.ProbeTarget,
			Weights:  balancer.ProfileFor(prof),
		}
		if _, err := ranker.Pick(ctx, ns, cands); err != nil {
			log.Warn("noderank: pick failed", "service", svc.Name, "err", err.Error())
			continue
		}
		// Apply this service's fresh advice now, rather than waiting for the whole
		// (potentially minutes-long) pass over every pool to finish.
		if nudge != nil {
			nudge(svc.Name)
		}
	}
	return nil
}

// demoTimeline scripts probe outcomes to exercise the full loop without
// hardware:
//
//	t < 3s : everything OK            -> youtube stays PREFERRED (pos 0)
//	t < 10s: only VPN (pos 2) OK      -> escalate 0->1->2 (PREFERRED->ALT_ZAPRET->VPN)
//	t >=10s: pos 1 fails, others OK   -> silent probes recover youtube to PREFERRED (pos 0)
func demoTimeline(_ string, position int, elapsed time.Duration) (bool, int) {
	const rtt = 20
	switch s := elapsed.Seconds(); {
	case s < 3:
		return true, rtt
	case s < 10:
		return position == 2, rtt
	default:
		return position != 1, rtt
	}
}
