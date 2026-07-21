package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/strace-me/lotsman/client/platform/nfqws"
	"github.com/strace-me/lotsman/client/platform/probe"
	"github.com/strace-me/lotsman/client/platform/rulesets"
	"github.com/strace-me/lotsman/pkg/applier"
	"github.com/strace-me/lotsman/pkg/audit"
	"github.com/strace-me/lotsman/pkg/brain"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/events"
	"github.com/strace-me/lotsman/pkg/executor"
	"github.com/strace-me/lotsman/pkg/faillog"
	"github.com/strace-me/lotsman/pkg/kb"
	"github.com/strace-me/lotsman/pkg/metrics"
	"github.com/strace-me/lotsman/pkg/probing"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

const (
	defaultQNum      = 200 // the router's nfqws queue number
	defaultConnbytes = 12  // desync only the first N packets of a connection
)

// Options configures a client Core.
type Options struct {
	ClashListen string              // loopback Clash-API host:port (default 127.0.0.1:9090)
	Interval    time.Duration       // probe + reassert interval (default 10s)
	KBFile      string              // KB persistence path ("" = in-memory)
	ProbeProxy  string              // socks addr to probe through the tunnel ("" = probe direct)
	RuleSetDir  string              // dir holding rule-set-{geosite,geoip}/*.srs ("" = generator default, i.e. the router's /etc/sing-box)
	Tun         *singbox.TunOptions // client ingress (nil = a sensible default tun)

	// ProxyListen switches the client to PROXY MODE: sing-box listens on this
	// socks address instead of capturing the system with a tun. Nothing is
	// intercepted and no root is needed — apps opt in by pointing at the proxy.
	// The safe way to try a config, and the only mode that works where a tun does
	// not. Probes are sent through the same inbound, so they measure what an app
	// using the proxy would experience.
	ProxyListen string

	// Desync (zapret/nfqws) — Linux only, and only when the config actually has
	// zapret chain steps. NFQUEUE needs root, which this service already has for
	// the tun, so it costs nothing extra where it works at all.
	NfqwsBin   string // nfqws binary ("" = "nfqws" on PATH)
	SingboxBin string // sing-box binary, used to decompile rule-sets into domains ("" = "sing-box")
	QNum       int    // NFQUEUE queue number (0 = 200, matching the router)
	WAN        string // egress interface for the nft rules ("" = autodetect the default route)
}

// Core is the single-device Lotsman client control plane. It reuses the daemon's
// brain/kb/applier/probing spine verbatim and drives sing-box via a ProxyCore
// plus the loopback Clash-API. The brain is the single writer of the data plane:
// it steers each service's sel-<svc> selector (VPN/direct) toward what the
// per-service probes show working — the "make it work" autonomy, no manual
// selector poking.
type Core struct {
	conf *config.Config
	reg  *registry.Registry
	box  ProxyCore
	opts Options
	log  *slog.Logger

	bus    *events.Bus
	kb     *kb.KB
	clash  *dataplane.ClashClient
	brain  *brain.Brain
	secret string

	zap       *nfqws.Engine // local desync engine (nil = no desync rung on this platform)
	tunnelIPs []string      // proxy server IPs the desync must never touch

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a client Core from a loaded config and a platform ProxyCore.
func New(conf *config.Config, box ProxyCore, opts Options, log *slog.Logger) *Core {
	if opts.ClashListen == "" {
		opts.ClashListen = "127.0.0.1:9090"
	}
	if opts.Interval == 0 {
		opts.Interval = 10 * time.Second
	}
	return &Core{conf: conf, reg: conf.Registry, box: box, opts: opts, log: log}
}

// Start generates the sing-box config, brings the box up, and launches the
// autonomy loop (brain + applier + prober). It returns once everything is
// running; call Stop to tear down.
func (c *Core) Start(ctx context.Context) error {
	c.bus = events.NewBus()
	c.kb = kb.New()

	// Strategy catalog bounds what the KB may propose.
	catalog := strategy.BuiltinCatalog()
	for _, d := range c.conf.Strategies {
		catalog.Add(d)
	}
	c.kb.SetZapretSeed(catalog.ZapretIDs())
	if c.opts.KBFile != "" {
		if err := c.kb.Load(c.opts.KBFile); err != nil {
			c.log.Warn("kb load failed (starting from seed)", "path", c.opts.KBFile, "err", err)
		}
	}

	// Mandatory random Clash-API secret: the generated config and this client agree
	// on it, so no co-resident process can drive the control plane without it.
	secret, err := randomSecret()
	if err != nil {
		return fmt.Errorf("core: secret: %w", err)
	}
	c.secret = secret
	c.clash = dataplane.NewClashClient("http://"+c.opts.ClashListen, secret)

	// Generate the client config (tun ingress + secret) and bring sing-box up.
	cfgJSON, err := c.generate(ctx)
	if err != nil {
		return fmt.Errorf("core: generate config: %w", err)
	}
	if err := c.box.Check(ctx, cfgJSON); err != nil {
		return fmt.Errorf("core: config check: %w", err)
	}
	if err := c.box.Start(ctx, cfgJSON); err != nil {
		return fmt.Errorf("core: start box: %w", err)
	}
	// The box binds its control port a moment AFTER the process starts. Without
	// this gate the brain's very first apply races the socket and fails, leaving
	// the client inert until the next reassert tick.
	if err := c.waitControlReady(ctx, 15*time.Second); err != nil {
		return err
	}

	// Brain + applier: VPN/direct steering over the Clash selector is the whole
	// "make it work" autonomy for the MVP (desync engines come later). The applier
	// is the single writer; NewVPN/NewDirect flip sel-<svc> over the Clash-API.
	execs := []executor.StrategyExecutor{
		executor.NewVPN(c.clash, false, c.log),
		executor.NewDirect(c.clash, false, c.log),
	}
	if z := c.newZapretExec(ctx); z != nil {
		execs = append(execs, z)
	}
	c.brain = brain.New(c.bus, c.reg, c.kb, brain.DefaultConfig(), audit.Nop{}, c.log)
	// Re-converge selectors every interval so a box restart (which resets them)
	// heals without waiting for a state transition. Idempotent, single writer.
	c.brain.SetReassert(c.opts.Interval)
	ap := applier.New(c.bus, execs, c.log)

	// Prober: test each service (through the tunnel when ProbeProxy is set), with
	// per-rung probe overrides, exactly as the daemon wires it.
	specs := map[string]dataplane.ServiceProbe{}
	for name, svc := range c.reg.Services {
		specs[name] = dataplane.ServiceProbe{Type: svc.ProbeType, Target: svc.ProbeTarget}
	}
	probeVia := c.opts.ProbeProxy
	if probeVia == "" && c.opts.ProxyListen != "" {
		// Proxy mode: probe THROUGH the box. Probing direct would measure the host's
		// own internet and call a dead node healthy — the brain would then never
		// escalate away from it.
		probeVia = c.opts.ProxyListen
	}
	mp := dataplane.NewMultiProberProxy(specs, probeVia)
	for name, svc := range c.reg.Services {
		for _, step := range svc.Chain {
			if step.ProbeType != "" {
				mp.OverrideRung(name, step.Position, dataplane.ServiceProbe{Type: step.ProbeType, Target: step.ProbeTarget})
			}
		}
	}
	// Rung-aware: an INACTIVE vpn rung cannot be measured through the selector (the
	// traffic would follow the active rung and credit the wrong one), so those are
	// measured with a Clash delay test of the pool instead.
	rp := probe.New(mp, c.clash, c.reg, "", 5*time.Second)

	mc := metrics.New(c.brain.Snapshot, c.kb.Snapshot)
	eng := probing.New(c.bus, rp, c.brain, c.reg, c.kb, mc, faillog.Nop{}, c.opts.Interval, c.log)

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	for _, run := range []func(context.Context){c.brain.Run, ap.Run, eng.Run} {
		c.wg.Add(1)
		go func(fn func(context.Context)) {
			defer c.wg.Done()
			fn(runCtx)
		}(run)
	}
	c.log.Info("lotsman client started", "services", len(c.reg.Services), "clash", c.opts.ClashListen)
	return nil
}

// Stop cancels the autonomy loop, stops sing-box, and persists the KB.
func (c *Core) Stop() error {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
	if c.zap != nil {
		if err := c.zap.Stop(context.Background()); err != nil {
			c.log.Warn("nfqws stop", "err", err)
		}
	}
	_ = c.box.Stop(context.Background())
	if c.opts.KBFile != "" && c.kb != nil {
		if err := c.kb.Save(c.opts.KBFile); err != nil {
			c.log.Warn("kb save failed", "path", c.opts.KBFile, "err", err)
		}
	}
	return nil
}

// NodeStatus is one service's currently-active node and brain state.
type NodeStatus struct {
	Service string `json:"service"`
	State   string `json:"state"`
	Node    string `json:"node"`
}

// Status reports each service's brain position plus the concrete node its
// selector currently points at (read over the Clash-API).
func (c *Core) Status(ctx context.Context) []NodeStatus {
	if c.brain == nil {
		return nil
	}
	var out []NodeStatus
	for _, s := range c.brain.Snapshot() {
		node := ""
		if info, err := c.clash.Proxy(ctx, registry.SelectorTag(s.Service)); err == nil {
			node = info.Now
		}
		out = append(out, NodeStatus{Service: s.Service, State: s.State, Node: node})
	}
	return out
}

// RenderConfig generates the sing-box config the client would run, WITHOUT
// starting anything — for `-print-config` inspection. It mints a fresh random
// secret if Start has not already set one.
func (c *Core) RenderConfig(ctx context.Context) ([]byte, error) {
	if c.secret == "" {
		s, err := randomSecret()
		if err != nil {
			return nil, err
		}
		c.secret = s
	}
	return c.generate(ctx)
}

// generate fetches subscription nodes and renders the client sing-box config
// (tun ingress + the mandatory Clash secret), reusing the daemon's exact path.
func (c *Core) generate(ctx context.Context) ([]byte, error) {
	if err := c.ensureRuleSets(ctx); err != nil {
		return nil, err
	}
	mgr := subscription.NewManager(subscription.NewHTTPFetcher())
	nodes, errs := mgr.Load(ctx, c.conf.Subscriptions)
	for _, e := range errs {
		c.log.Warn("subscription load issue", "err", e)
	}
	memberships := c.conf.Pools.Memberships(nodes)
	c.tunnelIPs = tunnelIPs(nodes, c.log)

	services := make([]registry.Service, 0, len(c.reg.Services))
	for _, s := range c.reg.Services {
		services = append(services, s)
	}

	opts := singbox.DefaultOptions()
	opts.ClashAPIListen = c.opts.ClashListen
	opts.ClashAPISecret = c.secret
	// Router-only: a Linux fwmark for the box's own traffic. sing-box refuses to
	// start with it on darwin/windows, and a tun client does not need it.
	opts.DefaultMark = 0
	// The client provisions its own .srs (the router's /etc/sing-box does not exist here).
	if c.opts.RuleSetDir != "" {
		opts.RuleSetDir = c.opts.RuleSetDir
	}
	if c.opts.ProxyListen != "" {
		// Proxy mode: no tun, no tproxy — just the socks inbound.
		opts.Tun = nil
		opts.TproxyPort = 0
		opts.SocksProbeListen = c.opts.ProxyListen
	} else {
		opts.Tun = c.opts.Tun
		if opts.Tun == nil {
			opts.Tun = &singbox.TunOptions{MTU: 9000, Address: []string{"172.19.0.1/30"}, Stack: "system", AutoRoute: true}
		}
		if c.opts.ProbeProxy != "" {
			opts.SocksProbeListen = c.opts.ProbeProxy
		}
	}

	res, err := singbox.Generate(services, c.conf.Devices, nodes, memberships, opts)
	if err != nil {
		return nil, err
	}
	return res.JSON, nil
}

// ensureRuleSets provisions the .srs files this config's services reference. The
// router gets them from its own updater into /etc/sing-box; a client has none, so
// it fetches the same upstream bundle and extracts only the tags in use. Warm
// cache = no-op, so this is cheap on every start.
func (c *Core) ensureRuleSets(ctx context.Context) error {
	if c.opts.RuleSetDir == "" {
		return nil // using the generator default (router layout); nothing to provision
	}
	seen := map[string]bool{}
	var tags []string
	for _, svc := range c.reg.Services {
		for _, t := range svc.RuleSets {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return rulesets.Ensure(ctx, c.opts.RuleSetDir, tags, c.log)
}

// waitControlReady blocks until the box answers on its loopback Clash-API, so the
// autonomy loop never drives a control plane that is not listening yet.
func (c *Core) waitControlReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		// "direct" is present in every config we generate, so it is a safe probe of
		// "is the API answering and is our secret accepted".
		if _, err := c.clash.Proxy(ctx, "direct"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("core: sing-box control API did not come up within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// newZapretExec builds the zapret-class executor when this platform and config can
// actually support local desync. NFQUEUE is Linux-only, and there is no point
// wiring a rung no service declares. Returning nil simply means the brain never
// offers that rung — it escalates straight to VPN instead.
func (c *Core) newZapretExec(ctx context.Context) executor.StrategyExecutor {
	if !c.hasZapretStep() {
		return nil
	}
	if runtime.GOOS != "linux" {
		c.log.Warn("desync rung unavailable: nfqws/NFQUEUE needs Linux", "goos", runtime.GOOS)
		return nil
	}
	wan := c.opts.WAN
	if wan == "" {
		detected, err := nfqws.DetectWAN(ctx)
		if err != nil {
			c.log.Warn("desync rung unavailable: cannot detect the WAN interface", "err", err)
			return nil
		}
		wan = detected
	}
	qnum := c.opts.QNum
	if qnum == 0 {
		qnum = defaultQNum
	}
	inst := zapret.Instance{
		Name:      "lotsman",
		QNum:      qnum,
		Capture:   zapret.Capture{TCP: []string{"80", "443", "2053", "2083", "2087", "2096", "8443"}, UDP: []string{"443"}},
		Connbytes: defaultConnbytes,
	}
	c.zap = nfqws.New(c.opts.NfqwsBin, inst, zapret.NftOptions{
		Table: "inet lotsman", WAN: wan, VPNServers: c.tunnelIPs,
	}, c.log)
	c.log.Info("desync rung enabled", "engine", "nfqws", "wan", wan, "qnum", qnum, "excluded_tunnels", len(c.tunnelIPs))

	return &zapretExec{
		clash:   c.clash,
		engine:  c.zap,
		recipes: append(strategycat.Load(), zaptune.RecipesFromDefinitions(c.conf.Strategies)...),
		active:  c.zapretServices,
		resolve: rulesets.NewResolver(c.opts.SingboxBin, c.opts.RuleSetDir, c.log).Resolve,
		log:     c.log,
	}
}

// hasZapretStep reports whether any service declares a zapret chain step.
func (c *Core) hasZapretStep() bool {
	for _, svc := range c.reg.Services {
		for _, step := range svc.Chain {
			if step.StrategyClass == strategy.ClassZapret {
				return true
			}
		}
	}
	return false
}

// tunnelIPs resolves the proxy nodes' servers to IPs. The nft rules must let this
// traffic through untouched: desyncing the tunnel to the proxy would corrupt the
// very transport the VPN rung depends on. The router hardcodes the same exclusion
// as VPN_SERVERS; a client derives it from the live node set instead.
func tunnelIPs(nodes []subscription.Node, log *slog.Logger) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, n := range nodes {
		if n.Server == "" {
			continue
		}
		if net.ParseIP(n.Server) != nil {
			add(n.Server)
			continue
		}
		addrs, err := net.LookupIP(n.Server)
		if err != nil {
			log.Warn("cannot resolve node server for desync exclusion", "server", n.Server, "err", err)
			continue
		}
		for _, a := range addrs {
			add(a.String())
		}
	}
	return out
}

func randomSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
