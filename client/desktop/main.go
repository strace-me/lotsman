// Command lotsman-client is the headless desktop client (Linux/Windows): it
// loads a config, generates a sing-box config with a random Clash-API secret,
// brings a local sing-box up, and runs the autonomy loop — the one-button "make
// it work" core that steers nodes/selectors itself, before the Wails UI lands.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/strace-me/lotsman/client/core"
	"github.com/strace-me/lotsman/client/platform/externalbox"
	"github.com/strace-me/lotsman/pkg/config"
)

func main() {
	var (
		configPath    = flag.String("config", "", "path to the lotsman client config (required)")
		singboxBin    = flag.String("singbox-bin", "sing-box", "sing-box binary")
		singboxCfg    = flag.String("singbox-config", "singbox.json", "where to write the live sing-box config")
		clashListen   = flag.String("clash", "127.0.0.1:9090", "loopback Clash-API host:port")
		probeProxy    = flag.String("probe-proxy", "", `socks addr to probe through the tunnel ("" = probe direct)`)
		interval      = flag.Duration("interval", 10*time.Second, "probe + reassert interval")
		kbFile        = flag.String("kb-file", "", `KB persistence path ("" = in-memory)`)
		ruleSetDir    = flag.String("ruleset-dir", "", "dir holding rule-set-{geosite,geoip}/*.srs (the client provisions these; empty = generator default)")
		nfqwsBin      = flag.String("nfqws-bin", "nfqws", "nfqws binary for the local desync rung (Linux only)")
		qnum          = flag.Int("qnum", 200, "NFQUEUE queue number for nfqws")
		wan           = flag.String("wan", "", "egress interface for the desync nft rules (empty = autodetect the default route)")
		desyncExclude = flag.String("desync-exclude", "", "comma-separated IPs/CIDRs the desync must never touch (the servers of another VPN sharing this host)")
		desyncForce   = flag.Bool("desync-force", false, "arm the desync even though another tunnel is present on this host")
		proxyListen   = flag.String("proxy", "", "PROXY MODE: run sing-box on this socks address instead of capturing the system with a tun (no root needed, nothing intercepted)")
		printConfig   = flag.Bool("print-config", false, "generate the sing-box config from -config, print it, and exit (no sing-box needed)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
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

	conf, err := config.Load(*configPath)
	if err != nil {
		log.Error("config load failed", "path", *configPath, "err", err)
		os.Exit(1)
	}

	box := externalbox.New(*singboxBin, *singboxCfg, log)
	c := core.New(conf, box, core.Options{
		ClashListen: *clashListen,
		Interval:    *interval,
		KBFile:      *kbFile,
		ProbeProxy:  *probeProxy,
		RuleSetDir:  *ruleSetDir,
		NfqwsBin:    *nfqwsBin,
		SingboxBin:  *singboxBin,
		QNum:        *qnum,
		WAN:         *wan,
		ProxyListen: *proxyListen,
		DesyncForce: *desyncForce,
		DesyncExclude: func() []string {
			if *desyncExclude == "" {
				return nil
			}
			return strings.Split(*desyncExclude, ",")
		}(),
	}, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
