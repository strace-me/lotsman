// Command lotsman-client is the headless desktop client (Linux/Windows): it
// loads a config, generates a sing-box config with a random Clash-API secret,
// brings a local sing-box up, and runs the autonomy loop — the one-button "make
// it work" core that steers nodes/selectors itself, before the Wails UI lands.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
func writeStarter(path, subCSV string) error {
	if path == "" {
		return fmt.Errorf("-init needs -config <path> to write to")
	}
	var subs []string
	for _, u := range strings.Split(subCSV, ",") {
		if u = strings.TrimSpace(u); u != "" {
			subs = append(subs, u)
		}
	}
	if len(subs) == 0 {
		return fmt.Errorf("-init needs at least one -sub <url>")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite it", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	out := scaffold.Starter(subs)
	if _, err := config.Parse(out); err != nil {
		return fmt.Errorf("generated config did not validate (a bug): %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 0600: the config will hold subscription URLs and, once fetched, node creds.
	return os.WriteFile(path, out, 0o600)
}

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
		ruleSetDir    = flag.String("ruleset-dir", "", "dir holding rule-set-{geosite,geoip}/*.srs (the client provisions these; empty = generator default)")
		nfqwsBin      = flag.String("nfqws-bin", "nfqws", "nfqws binary for the local desync rung (Linux only)")
		zapretFiles   = flag.String("zapret-files", "", "dir with zapret's fake payload .bin files (recipes naming a missing payload are skipped)")
		qnum          = flag.Int("qnum", 200, "NFQUEUE queue number for nfqws")
		wan           = flag.String("wan", "", "egress interface for the desync nft rules (empty = autodetect the default route)")
		desyncExclude = flag.String("desync-exclude", "", "comma-separated IPs/CIDRs the desync must never touch (the servers of another VPN sharing this host)")
		desyncForce   = flag.Bool("desync-force", false, "arm the desync even though another tunnel is present on this host")
		hostlistDir   = flag.String("hostlist-dir", "", "dir for per-service nfqws hostlist files (default /var/lib/lotsman/hostlists as root). MUST stay readable after nfqws drops privileges, so a path under a 0700 home will not work. Empty and non-root inlines domains into argv instead, which costs an engine restart on every membership change")
		proxyListen   = flag.String("proxy", "", "PROXY MODE: run sing-box on this socks address instead of capturing the system with a tun (no root needed, nothing intercepted)")
		refreshEvery  = flag.Duration("refresh-interval", 5*time.Minute, "how often to re-fetch subscriptions and reconcile the config (0 = never)")
		controlSock   = flag.String("control-socket", "", "unix socket for the local control API (empty = default under the runtime dir)")
		metricsAddr   = flag.String("metrics-addr", "", "serve Prometheus /metrics on this host:port (empty = off; also enables the passive observe pass that surfaces frozen/leak/one-way flows)")
		baselineFile  = flag.String("baseline-file", "", "persist the reconciler anti-churn baseline here (empty = derive from -singbox-config; the guard then survives restarts)")
		initConfig    = flag.Bool("init", false, "write a starter config to -config from -sub URL(s) and exit (refuses to overwrite an existing file)")
		subURLs       = flag.String("sub", "", "comma-separated subscription URL(s) for -init")
		printConfig   = flag.Bool("print-config", false, "generate the sing-box config from -config, print it, and exit (no sing-box needed)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *initConfig {
		if err := writeStarter(*configPath, *subURLs); err != nil {
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

	box := externalbox.New(*singboxBin, *singboxCfg, log)
	c := core.New(conf, box, core.Options{
		ClashListen:   *clashListen,
		Interval:      *interval,
		KBFile:        *kbFile,
		StateFile:     *stateFile,
		ProbeProxy:    *probeProxy,
		RuleSetDir:    *ruleSetDir,
		NfqwsBin:      *nfqwsBin,
		ZapretFiles:   *zapretFiles,
		HostlistDir:   *hostlistDir,
		SingboxBin:    *singboxBin,
		SingboxConfig: *singboxCfg,
		RefreshEvery:  *refreshEvery,
		QNum:          *qnum,
		WAN:           *wan,
		ProxyListen:   *proxyListen,
		MetricsAddr:   *metricsAddr,
		BaselineFile:  *baselineFile,
		DesyncForce:   *desyncForce,
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

	if err := c.Start(ctx); err != nil {
		log.Error("client start failed", "err", err)
		os.Exit(1)
	}

	// The control socket is what an unprivileged tray attaches to. A failure here
	// must not take the tunnel down with it — the service is useful headless.
	ctl := core.NewControlServer(c, cancel, log)
	if err := ctl.Serve(*controlSock); err != nil {
		log.Warn("control API unavailable (continuing headless)", "err", err)
	} else {
		defer ctl.Close()
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
			return
		case <-ticker.C:
			for _, s := range c.Status(ctx) {
				log.Info("service", "name", s.Service, "state", s.State, "node", s.Node)
			}
		}
	}
}
