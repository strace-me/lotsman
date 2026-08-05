// Command lotsman-client is the headless desktop client (Linux/Windows): it
// loads a config, generates a sing-box config with a random Clash-API secret,
// brings a local sing-box up, and runs the autonomy loop — the one-button "make
// it work" core that steers nodes/selectors itself, before the Wails UI lands.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/strace-me/lotsman/pkg/version"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/strace-me/lotsman/client/core"
	"github.com/strace-me/lotsman/client/platform/externalbox"
	"github.com/strace-me/lotsman/client/scaffold"
	"github.com/strace-me/lotsman/pkg/config"
)

// writeStarter emits a working starter config to path from the given comma-
// separated subscription URLs. It refuses to overwrite an existing file — an
// -init that clobbered a tuned config would be a nasty surprise — and validates
// its own output before writing, so a bad scaffold fails here, not on first run.
func writeStarter(path, subCSV string, recommended bool) error {
	if path == "" {
		return fmt.Errorf("-init needs -config <path> to write to")
	}
	var subs []string
	for _, u := range strings.Split(subCSV, ",") {
		if u = strings.TrimSpace(u); u != "" {
			subs = append(subs, u)
		}
	}
	// The recommended config ships without creds (the user adds a subscription
	// later, e.g. in the app), so -sub is optional there; the minimal starter needs
	// at least one so it has a tunnel to point at.
	if !recommended && len(subs) == 0 {
		return fmt.Errorf("-init needs at least one -sub <url> (or pass -recommended for the curated default without creds)")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite it", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	out := scaffold.Starter(subs)
	if recommended {
		out = scaffold.Recommended(subs)
	}
	if _, err := config.Parse(out); err != nil {
		return fmt.Errorf("generated config did not validate (a bug): %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 0600: the config will hold subscription URLs and, once fetched, node creds.
	return os.WriteFile(path, out, 0o600)
}

// hostlistFetchTimeout bounds the one-off fetch of domain packs that have no file yet,
// so an unreachable source delays a fresh install by seconds instead of hanging it.
const hostlistFetchTimeout = 30 * time.Second

// startFailureGrace is how long the control socket stays up after a failed start, so a
// UI can attach, read why, and push a fix. Long enough to be useful to somebody already
// looking, short enough that the boot-before-network case still recovers promptly by
// exiting non-zero and letting the service manager retry.
const startFailureGrace = 30 * time.Second

func main() {
	var (
		configPath    = flag.String("config", "", "path to the lotsman client config (required)")
		singboxBin    = flag.String("singbox-bin", "sing-box", "sing-box binary")
		singboxCfg    = flag.String("singbox-config", "singbox.json", "where to write the live sing-box config")
		clashListen   = flag.String("clash", "127.0.0.1:9090", "loopback Clash-API host:port")
		probeProxy    = flag.String("probe-proxy", "", `socks addr to probe through the tunnel ("" = probe direct)`)
		interval      = flag.Duration("interval", 10*time.Second, "probe + reassert interval")
		stateFile     = flag.String("state-file", "", `persist chain positions across restarts ("" = start cold)`)
		kbFile        = flag.String("kb-file", "", `KB persistence path ("" = in-memory)`)
		kbDir         = flag.String("kb-dir", "", `per-network KB dir: the store becomes <dir>/<network-id>.json so each network's learning stays separate (overrides -kb-file)`)
		ruleSetDir    = flag.String("ruleset-dir", "", "dir holding rule-set-{geosite,geoip}/*.srs (the client provisions these; empty = generator default)")
		nfqwsBin      = flag.String("nfqws-bin", "nfqws", "nfqws binary for the local desync rung (Linux only)")
		zapretFiles   = flag.String("zapret-files", "", "dir with zapret's fake payload .bin files (recipes naming a missing payload are skipped)")
		qnum          = flag.Int("qnum", 200, "NFQUEUE queue number for nfqws")
		wan           = flag.String("wan", "", "egress interface for the desync nft rules (empty = autodetect the default route)")
		desyncExclude = flag.String("desync-exclude", "", "comma-separated IPs/CIDRs the desync must never touch (the servers of another VPN sharing this host)")
		desyncForce   = flag.Bool("desync-force", false, "arm the desync even though another tunnel is present on this host")
		hostDNS       = flag.Bool("host-dns", false, "TUN MODE: redirect the host's own resolver into the tunnel so its browser/shell resolve censored names through the trusted resolver (rewrites /etc/resolv.conf, restores on stop; needs root)")
		hostlistDir   = flag.String("hostlist-dir", "", "dir for per-service nfqws hostlist files (default /var/lib/lotsman/hostlists as root). MUST stay readable after nfqws drops privileges, so a path under a 0700 home will not work. Empty and non-root inlines domains into argv instead, which costs an engine restart on every membership change")
		proxyListen   = flag.String("proxy", "", "PROXY MODE: run sing-box on this socks address instead of capturing the system with a tun (no root needed, nothing intercepted)")
		refreshEvery  = flag.Duration("refresh-interval", 5*time.Minute, "how often to re-fetch subscriptions and reconcile the config (0 = never)")
		roamInterval  = flag.Duration("roam-interval", 15*time.Second, "how often to re-fingerprint the network and swap the KB on a change (only with -kb-dir)")
		controlSock   = flag.String("control-socket", "", "unix socket for the local control API (empty = default under the runtime dir)")
		controlGroup  = flag.String("control-socket-group", "", "UNIX group to own the control socket (0660, group-traversable dir) so an unprivileged UI in this group can drive a root service; empty = owner-only 0600")
		metricsAddr   = flag.String("metrics-addr", "", "serve Prometheus /metrics on this host:port (empty = off; also enables the passive observe pass that surfaces frozen/leak/one-way flows)")
		baselineFile  = flag.String("baseline-file", "", "persist the reconciler anti-churn baseline here (empty = derive from -singbox-config; the guard then survives restarts)")
		initConfig    = flag.Bool("init", false, "write a starter config to -config from -sub URL(s) and exit (refuses to overwrite an existing file)")
		subURLs       = flag.String("sub", "", "comma-separated subscription URL(s) for -init")
		recommended   = flag.Bool("recommended", false, "with -init: write the curated recommended config (services + rules, no creds); -sub optional")
		printConfig   = flag.Bool("print-config", false, "generate the sing-box config from -config, print it, and exit (no sing-box needed)")
	)
	canaryGoodput := flag.Float64("canary-goodput-kbps", 64, "a zapret recipe must sustain at least this KiB/s to be credited; reachability alone scores a frozen path as a win. 0 disables the check.")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("lotsman-client starting", "version", version.String())

	if *initConfig {
		if err := writeStarter(*configPath, *subURLs, *recommended); err != nil {
			log.Error("init failed", "err", err)
			os.Exit(1)
		}
		log.Info("wrote starter config", "path", *configPath, "next", "edit it, then run without -init")
		return
	}

	// Persist the anti-churn baseline beside the config the reconciler manages, so
	// the degraded-fetch guard works across restarts without the operator wiring a
	// path (LOT-45). Only meaningful when refresh actually runs.
	if *baselineFile == "" && *singboxCfg != "" {
		*baselineFile = *singboxCfg + ".baseline"
	}

	if *configPath == "" {
		log.Error("missing required -config")
		os.Exit(2)
	}
	// Default the rule-set cache to a per-user location so a first run provisions
	// itself instead of pointing at the router's /etc/sing-box.
	if *ruleSetDir == "" {
		if cache, cerr := os.UserCacheDir(); cerr == nil {
			*ruleSetDir = filepath.Join(cache, "lotsman", "rule-sets")
		}
	}

	// nfqws re-reads its hostlist AFTER dropping privileges, and refuses to start
	// when it cannot. A home directory is typically 0700, so the files have to live
	// somewhere the unprivileged user can still traverse.
	if *hostlistDir == "" && os.Geteuid() == 0 {
		*hostlistDir = "/var/lib/lotsman/hostlists"
	}

	if *controlSock == "" {
		base := os.Getenv("XDG_RUNTIME_DIR")
		if base == "" {
			base = os.TempDir()
		}
		*controlSock = filepath.Join(base, "lotsman", "control.sock")
	}

	conf, err := config.Load(*configPath)
	if err != nil {
		log.Error("config load failed", "path", *configPath, "err", err)
		os.Exit(1)
	}
	// A service's domain_lists are merged into its domains when the config is PARSED,
	// so a pack whose file has not been fetched yet is simply absent for the whole run.
	// Fetch the missing ones and re-parse once, before anything consumes the config —
	// doing it later (e.g. inside Start) cannot change a domain set that is already fixed.
	if core.EnsureDomainLists(context.Background(), conf, hostlistFetchTimeout, log) {
		if conf, err = config.Load(*configPath); err != nil {
			log.Error("config reload after fetching domain lists failed", "path", *configPath, "err", err)
			os.Exit(1)
		}
	}

	box := externalbox.New(*singboxBin, *singboxCfg, log)
	c := core.New(conf, box, core.Options{
		ClashListen:       *clashListen,
		Interval:          *interval,
		KBFile:            *kbFile,
		KBDir:             *kbDir,
		StateFile:         *stateFile,
		CanaryGoodputKBps: *canaryGoodput,
		ProbeProxy:        *probeProxy,
		RuleSetDir:        *ruleSetDir,
		NfqwsBin:          *nfqwsBin,
		ZapretFiles:       *zapretFiles,
		HostlistDir:       *hostlistDir,
		SingboxBin:        *singboxBin,
		SingboxConfig:     *singboxCfg,
		RefreshEvery:      *refreshEvery,
		RoamInterval:      *roamInterval,
		QNum:              *qnum,
		WAN:               *wan,
		ProxyListen:       *proxyListen,
		MetricsAddr:       *metricsAddr,
		BaselineFile:      *baselineFile,
		DesyncForce:       *desyncForce,
		HostDNS:           *hostDNS,
		DesyncExclude: func() []string {
			if *desyncExclude == "" {
				return nil
			}
			return strings.Split(*desyncExclude, ",")
		}(),
	}, log)

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	if *printConfig {
		js, err := c.RenderConfig(ctx)
		if err != nil {
			log.Error("render config failed", "err", err)
			os.Exit(1)
		}
		os.Stdout.Write(js)
		os.Stdout.Write([]byte("\n"))
		return
	}

	// The control socket goes up BEFORE the data plane, and deliberately so.
	//
	// It used to be bound only after a successful Start, which meant the one failure
	// the GUI's config editor exists to repair — a config the client refuses — was
	// exactly the failure that made the editor unreachable. The operator was left with
	// a service that exits, restarts, exits, and a UI that cannot attach to say why.
	//
	// A failure binding it must not take the tunnel down with it: the service is useful
	// headless. applyRestart lets the config-editing endpoint apply a saved config by
	// re-exec: it flags the intent, then cancels, so the run loop tears the data plane
	// down (Core.Stop) BEFORE we replace the process — no orphaned sing-box/nfqws.
	var applyRestart atomic.Bool
	restart := func() { applyRestart.Store(true); cancel() }
	ctl := core.NewControlServer(c, cancel, log).WithConfig(*configPath, restart)
	if *controlGroup != "" {
		ctl = ctl.WithSocketGroup(*controlGroup)
	}
	if err := ctl.Serve(*controlSock); err != nil {
		log.Warn("control API unavailable (continuing headless)", "err", err)
	} else {
		defer ctl.Close()
	}

	if err := c.Start(ctx); err != nil {
		// Hold the socket open for a moment before giving up, so a UI that is attached
		// (or attaches now) can read the failure and push a corrected config, which
		// re-execs us into a clean process — Core.Start is not re-entrant, so retrying
		// it in place is not an option. Then exit non-zero: a service manager retries,
		// and the boot-before-network case recovers on its own.
		log.Error("client start failed — the control socket stays up briefly so a UI can read this and fix the config",
			"err", err, "grace", startFailureGrace)
		select {
		case <-ctx.Done(): // a config save (or a signal) arrived
		case <-time.After(startFailureGrace):
		}
		if applyRestart.Load() {
			ctl.Close()
			reexec(log)
		}
		os.Exit(1)
	}

	// Report the active node per service each interval until signalled.
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			if err := c.Stop(); err != nil {
				log.Warn("stop", "err", err)
			}
			if applyRestart.Load() {
				// A config save asked us to re-exec: the data plane is down now, so a
				// fresh copy of this process loads the new config with no orphans.
				reexec(log)
			}
			return
		case <-ticker.C:
			for _, s := range c.Status(ctx) {
				log.Info("service", "name", s.Service, "state", s.State, "node", s.Node)
			}
		}
	}
}
