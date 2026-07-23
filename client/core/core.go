package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/strace-me/lotsman/client/platform/nfqws"
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
	"github.com/strace-me/lotsman/pkg/observe"
	"github.com/strace-me/lotsman/pkg/probing"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/state"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// defaultTunExcludes keep link-local discovery and the LAN out of the tunnel.
// A VPN that swallows multicast silently breaks every neighbour-discovery
// protocol on the machine, and the failure looks like anything but routing.
var defaultTunExcludes = []string{
	"224.0.0.0/4",        // all IPv4 multicast: mDNS, SSDP, LocalSend, Chromecast
	"ff00::/8",           // the IPv6 equivalent
	"255.255.255.255/32", // limited broadcast
}

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
	NfqwsBin      string        // nfqws binary ("" = "nfqws" on PATH)
	ZapretFiles   string        // dir holding zapret's fake payload .bin files ("" = skip the availability check)
	HostlistDir   string        // dir for per-service nfqws hostlist files ("" = inline domains into argv, which forces an engine restart on every membership change)
	SingboxBin    string        // sing-box binary, used to decompile rule-sets into domains ("" = "sing-box")
	SingboxConfig string        // path of the live sing-box config the reconciler swaps
	StateFile     string        // persists each service's chain position across restarts ("" = start cold every time)
	RefreshEvery  time.Duration // how often to re-fetch subscriptions and reconcile (0 = never)
	QNum          int           // NFQUEUE queue number (0 = 200, matching the router)
	WAN           string        // egress interface for the nft rules ("" = autodetect the default route)

	// MetricsAddr serves the same Prometheus /metrics the daemon exposes (per-service
	// position/state/broken/active-fails, KB EWMA, and — while it is set — the passive
	// observation eye's leak/dead/frozen/one-way ratios). "" = do not serve, and skip
	// the observe pass entirely so nothing is computed for a surface nobody reads.
	MetricsAddr string
	// BaselineFile persists the anti-churn baseline (the node count of the last clean
	// apply) so the reconciler's degraded-fetch guard survives a restart instead of
	// applying a degraded startup fetch wholesale. "" = in-memory only (the guard is
	// then inert on the first tick after every restart — LOT-29/LOT-45).
	BaselineFile string

	// DesyncExclude are extra destinations nfqws must never touch, on top of this
	// client's own nodes — the servers of any OTHER tunnel sharing this host.
	// They cannot be discovered automatically (see nfqws.ForeignTunnels), so an
	// operator running a second VPN has to name them.
	DesyncExclude []string
	// DesyncForce arms the desync even though another tunnel is present. Only
	// meaningful once its endpoints are in DesyncExclude, or its traffic does not
	// cross the queued ports.
	DesyncForce bool
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

	metrics    *metrics.Collector // populated in Start; nil until then
	metricsSrv *http.Server       // non-nil only when MetricsAddr is served
	obsMu      sync.Mutex         // guards obsSnap between the observe loop and /metrics
	obsSnap    observe.Snapshot   // last passive-observation snapshot (zero until the first pass)

	prober dataplane.Prober // shared with the autonomy loop, reused by the desync canary
	// foreignTunnels is sampled BEFORE our own tun exists. Sampling it later would
	// always find our own interface and disable the desync rung in tun mode.
	foreignTunnels []string
	zap            *nfqws.Engine // local desync engine (nil = no desync rung on this platform)
	zapExec        *zapretExec   // the zapret executor, reconciled periodically (nil = no desync rung)
	tunnelIPs      []string      // proxy server IPs the desync must never touch
	lastNodes      int           // nodes the last generate loaded; zero with subscriptions declared means no tunnel

	mu         sync.Mutex
	life       context.Context
	lifeCancel context.CancelFunc
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// New builds a client Core from a loaded config and a platform ProxyCore.
func New(conf *config.Config, box ProxyCore, opts Options, log *slog.Logger) *Core {
	if opts.ClashListen == "" {
		opts.ClashListen = "127.0.0.1:9090"
	}
	if opts.Interval == 0 {
		opts.Interval = 10 * time.Second
	}
	// nfqws resolves relative paths against ITS working directory, which is
	// whatever launched the service — under systemd that is not where the operator
	// ran the command. A relative hostlist or payload path therefore works when
	// tested by hand and silently fails to load once installed, taking the desync
	// with it and saying nothing.
	opts.HostlistDir = absDir(opts.HostlistDir)
	opts.ZapretFiles = absDir(opts.ZapretFiles)
	opts.SingboxConfig = absDir(opts.SingboxConfig)
	return &Core{conf: conf, reg: conf.Registry, box: box, opts: opts, log: log}
}

// Start generates the sing-box config, brings the box up, and launches the
// autonomy loop (brain + applier + prober). It returns once everything is
// running; call Stop to tear down.
func (c *Core) Start(ctx context.Context) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// One lifetime for everything this client spawns, so a background verdict
	// cannot outlive the data plane it is judging.
	c.mu.Lock()
	c.life, c.lifeCancel = context.WithCancel(ctx)
	c.mu.Unlock()

	// Sample the host's tunnels while the only ones present belong to somebody
	// else — after box.Start our own tun is up and would be mistaken for a foreign
	// one, silently disabling the desync rung in exactly the mode that needs it.
	c.foreignTunnels = nfqws.ForeignTunnels()

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
	// Every selector falls back to "direct" when the fetch came back empty, so the
	// client would start, report itself healthy and carry no traffic through any
	// tunnel at all. A declared subscription that yields nothing is a failure, not
	// a configuration.
	if len(c.conf.Subscriptions) > 0 && c.lastNodes == 0 {
		return fmt.Errorf("core: %d subscription(s) declared but not one node was loaded — "+
			"refusing to start with no tunnel (check connectivity and the subscription URLs)",
			len(c.conf.Subscriptions))
	}
	if err := c.box.Check(ctx, cfgJSON); err != nil {
		return fmt.Errorf("core: config check: %w", err)
	}
	if err := c.box.Start(ctx, cfgJSON); err != nil {
		return fmt.Errorf("core: start box: %w", err)
	}
	// From here on the box owns a tun with auto_route. Any failure that returns
	// without stopping it leaves the host's routing hijacked by a process nothing
	// supervises, and main exits without ever calling Stop.
	started := false
	defer func() {
		if !started {
			if err := c.box.Stop(context.WithoutCancel(ctx)); err != nil {
				c.log.Warn("could not stop sing-box after a failed start", "err", err)
			}
		}
	}()
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
		c.zapExec = z
		execs = append(execs, z)
	}
	c.dropUnsupportedRungs(execs)
	c.brain = brain.New(c.bus, c.reg, c.kb, brain.DefaultConfig(), audit.Nop{}, c.log)
	// Re-converge selectors every interval so a box restart (which resets them)
	// heals without waiting for a state transition. Idempotent, single writer.
	c.brain.SetReassert(c.opts.Interval)
	// Persist which rung each service settled on. Without it every restart begins
	// at the top of the chain and re-escalates through the failures that were
	// already paid for once — the KB remembers which strategy works, but not that
	// this service had already been moved off the rung that does not.
	if c.opts.StateFile != "" {
		store := state.NewFileStore(c.opts.StateFile)
		c.brain.Restore(store.Load())
		c.brain.SetPersist(func(pos map[string]int) { store.Save(pos) })
		c.log.Info("chain positions persisted across restarts", "path", c.opts.StateFile)
	}
	ap := applier.New(c.bus, execs, c.log)

	// Prober: test each service (through the tunnel when ProbeProxy is set), with
	// per-rung probe overrides, exactly as the daemon wires it.
	specs := map[string]dataplane.ServiceProbe{}
	for name, svc := range c.reg.Services {
		specs[name] = dataplane.ServiceProbe{Type: svc.ProbeType, Target: svc.ProbeTarget}
	}
	probeVia := c.opts.ProbeProxy
	if c.opts.ProxyListen != "" {
		// Proxy mode has ONE socks inbound (ProxyListen); singboxOptions points the
		// box's probe inbound at exactly that address. The prober must dial the same
		// one, or it reaches a port nothing is listening on and every probe fails.
		// ProbeProxy is a tun-mode knob (a separate probe-through-the-tunnel inbound)
		// and does not apply here, so it must not win — probe THROUGH the box, since
		// probing direct would measure the host's own internet and call a dead node
		// healthy, and the brain would never escalate away from it.
		if probeVia != "" && probeVia != c.opts.ProxyListen {
			c.log.Warn("ignoring -probe-proxy in proxy mode; probing through -proxy instead",
				"probe_proxy", probeVia, "proxy", c.opts.ProxyListen)
		}
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
	rp := dataplane.NewRungProber(mp, c.clash, c.reg, "", 0)

	c.prober = rp
	mc := metrics.New(c.brain.Snapshot, c.kb.Snapshot)
	c.metrics = mc
	if c.opts.MetricsAddr != "" {
		// Wire the snapshot getter BEFORE any scrape can arrive; the observe loop only
		// updates the guarded value from then on.
		mc.SetObserveSnapshot(func() observe.Snapshot {
			c.obsMu.Lock()
			defer c.obsMu.Unlock()
			return c.obsSnap
		})
	}
	eng := probing.New(c.bus, rp, c.brain, c.reg, c.kb, mc, faillog.Nop{}, c.opts.Interval, c.log)

	c.mu.Lock()
	runCtx, cancel := c.life, c.lifeCancel
	c.cancel = cancel
	c.mu.Unlock()
	runners := []func(context.Context){c.brain.Run, ap.Run, eng.Run, c.superviseBox}
	if c.opts.RefreshEvery > 0 && c.opts.SingboxConfig != "" {
		rc := c.newReconciler()
		runners = append(runners, func(rctx context.Context) { c.refreshLoop(rctx, rc, c.opts.RefreshEvery) })
		c.log.Info("subscription refresh enabled", "every", c.opts.RefreshEvery)
	}
	// Observability: only when a scraper is actually served. The eye reads the
	// Clash /connections list each interval to surface leak/dead/frozen/one-way
	// flows — the TSPU-freeze signal a header-only probe cannot see — and feeds it
	// to the collector. Telemetry only: it does not (yet) drive escalation on the
	// client, so nothing behavioural changes by turning it on.
	if c.opts.MetricsAddr != "" {
		runners = append(runners, c.observeLoop)
	}
	if c.zapExec != nil {
		// The StrategyExecutor interface only reports a service ENTERING a rung, so
		// nothing tells the desync engine when the LAST service leaves it. Reconcile
		// it against the live rung membership each interval: recompose when the set
		// changed, stop the engine when it emptied (LOT-46). Cheap and idempotent —
		// the resolver is cached and Apply is a no-op when the argv is unchanged.
		runners = append(runners, c.desyncReconcileLoop)
	}
	for _, run := range runners {
		c.wg.Add(1)
		go func(fn func(context.Context)) {
			defer c.wg.Done()
			fn(runCtx)
		}(run)
	}
	if c.opts.MetricsAddr != "" {
		// Bind synchronously so the "enabled" log asserts a listener that actually
		// came up. mc.Serve runs ListenAndServe in a goroutine and drops its bind
		// error, which would log success over a dead endpoint (e.g. a port already in
		// use). A failed metrics bind is non-fatal — it is telemetry, not the tunnel.
		if ln, lerr := net.Listen("tcp", c.opts.MetricsAddr); lerr != nil {
			c.log.Warn("metrics endpoint unavailable (continuing without it)", "addr", c.opts.MetricsAddr, "err", lerr)
		} else {
			mux := http.NewServeMux()
			mux.Handle("/metrics", mc)
			c.metricsSrv = &http.Server{Handler: mux}
			go c.metricsSrv.Serve(ln)
			c.log.Info("metrics enabled", "addr", c.opts.MetricsAddr, "path", "/metrics")
		}
	}
	started = true
	c.log.Info("lotsman client started", "services", len(c.reg.Services), "clash", c.opts.ClashListen)
	return nil
}

// clashObserveSource adapts the loopback Clash-API to the passive eye's Source,
// mapping each /connections record to the subset observe needs. It mirrors the
// daemon's adapter; the eye is otherwise the daemon's verbatim.
type clashObserveSource struct{ c *dataplane.ClashClient }

func (s clashObserveSource) Connections(ctx context.Context) ([]observe.Conn, error) {
	cs, err := s.c.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]observe.Conn, len(cs))
	for i, c := range cs {
		port, _ := strconv.Atoi(c.Metadata.DestinationPort) // 0 on parse failure (best-effort)
		out[i] = observe.Conn{
			ID:     c.ID,
			Chains: c.Chains, Upload: c.Upload, Download: c.Download, Rule: c.Rule,
			Host: c.Metadata.Host, DestIP: c.Metadata.DestinationIP, DestPort: port, Network: c.Metadata.Network,
		}
	}
	return out, nil
}

// desyncReconcileLoop keeps the nfqws engine in step with the set of services
// currently on the zapret rung, closing the gap that the enter-only executor
// interface leaves: when the last service escalates away, this is what stops the
// engine instead of leaving it desyncing traffic for services long gone.
func (c *Core) desyncReconcileLoop(ctx context.Context) {
	t := time.NewTicker(c.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.zapExec.Reconcile(ctx); err != nil {
				c.log.Warn("desync reconcile failed (retrying next tick)", "err", err)
			}
		}
	}
}

// observeLoop feeds the metrics collector the passive eye's snapshot each
// interval. It runs only when metrics are served (see Start) and never touches
// escalation — a failed pass (sing-box down, no connections yet) is logged and
// retried, exactly as the daemon treats it.
func (c *Core) observeLoop(ctx context.Context) {
	eye := observe.New(clashObserveSource{c.clash}, c.reg)
	t := time.NewTicker(c.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s, err := eye.Observe(ctx)
			if err != nil {
				c.log.Debug("observe pass failed (retrying next tick)", "err", err)
				continue
			}
			c.obsMu.Lock()
			c.obsSnap = s
			c.obsMu.Unlock()
		}
	}
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
	if c.metricsSrv != nil {
		c.metricsSrv.Close()
	}
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

// NodeStatus is one service's currently-active node and brain state. Fails and
// Broken let a UI tell a healthy service from a wedged one: State/Node alone show
// WHERE a service sits, not whether it is actually working there.
type NodeStatus struct {
	Service string `json:"service"`
	State   string `json:"state"`
	Node    string `json:"node"`
	Fails   int    `json:"fails"`  // consecutive active-probe failures at this rung
	Broken  bool   `json:"broken"` // chain exhausted — escalated past the last rung and still failing
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
		out = append(out, NodeStatus{Service: s.Service, State: s.State, Node: node, Fails: s.Fails, Broken: s.Broken})
	}
	return out
}

// Healthy reports whether the data plane is actually up: the sing-box process is
// alive AND its control plane answers. Reporting "running" from the mere presence
// of a config (services != nil) would show green over a dead or wedged sing-box —
// exactly what a tray must not do (LOT-49).
func (c *Core) Healthy(ctx context.Context) bool {
	if c.box == nil || !c.box.Alive(ctx) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.clash.Proxy(ctx, "direct")
	return err == nil
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

// singboxOptions is the generator configuration this client runs with. Both the
// startup render and the reconciler must use the SAME options — a divergence
// would make every reconcile see a spurious diff and restart sing-box forever,
// and a regenerated Clash secret would lock this client out of its own box.
func (c *Core) singboxOptions() singbox.Options {
	opts := singbox.DefaultOptions()
	// The reconciler derives these from the pool set on every pass; leaving them
	// unset here makes the startup config differ from the first reconciled one and
	// restarts sing-box for a difference that is not real.
	opts.PoolOpts = singbox.PoolOptionsFrom(c.conf.Pools)
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
		opts.Tun = c.tunOptions()
		if c.opts.ProbeProxy != "" {
			opts.SocksProbeListen = c.opts.ProbeProxy
		}
	}

	return opts
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
	c.lastNodes = len(nodes)

	services := make([]registry.Service, 0, len(c.reg.Services))
	for _, s := range c.reg.Services {
		services = append(services, s)
	}

	opts := c.singboxOptions()

	res, err := singbox.Generate(services, c.conf.Devices, nodes, memberships, opts)
	if err != nil {
		return nil, err
	}
	return res.JSON, nil
}

// tunOptions is the ingress this client would claim in tun mode — the caller's
// choice, or a default. preflight and generate must agree on it, so it lives in
// one place.
func (c *Core) tunOptions() *singbox.TunOptions {
	if c.opts.Tun != nil {
		return c.opts.Tun
	}
	return &singbox.TunOptions{
		MTU: 9000, Address: []string{"172.19.0.1/30"}, Stack: "system", AutoRoute: true,
		ExcludeRoutes: defaultTunExcludes,
	}
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
func (c *Core) newZapretExec(ctx context.Context) *zapretExec {
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
	// A foreign tunnel's packets are indistinguishable from ordinary traffic here,
	// so arming the desync would silently mangle it. Refuse the RUNG rather than
	// the client: the brain simply escalates to VPN instead.
	if foreign := c.foreignTunnels; len(foreign) > 0 && !c.opts.DesyncForce {
		if len(c.opts.DesyncExclude) == 0 {
			c.log.Warn("desync rung DISABLED: another tunnel is on this host and nfqws would mangle it",
				"interfaces", foreign,
				"fix", "list that tunnel's server IPs in -desync-exclude, or pass -desync-force if its traffic does not cross the queued ports")
			return nil
		}
		c.log.Info("desync rung armed alongside a foreign tunnel",
			"interfaces", foreign, "excluded", c.opts.DesyncExclude)
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
	// Never desync a tunnel: ours (derived from the live node set) or a foreign
	// one the operator named.
	excluded := append(append([]string{}, c.tunnelIPs...), c.opts.DesyncExclude...)
	c.zap = nfqws.New(c.opts.NfqwsBin, inst, zapret.NftOptions{
		Table: "inet lotsman", WAN: wan, VPNServers: excluded,
	}, c.log)
	c.log.Info("desync rung enabled", "engine", "nfqws", "wan", wan, "qnum", qnum, "excluded_tunnels", len(excluded))

	return &zapretExec{
		clash:  c.clash,
		engine: c.zap,
		recipes: usablePayloadRecipes(
			append(strategycat.Load(), zaptune.RecipesFromDefinitions(c.conf.Strategies)...),
			c.opts.ZapretFiles, c.log),
		// Rank recipes by what has actually worked for this service. A cold KB scores
		// every candidate at the prior, so this degrades to catalog order — the same
		// choice the seed picker makes — and sharpens only as outcomes accumulate.
		pick: zaptune.KBPicker(func(service, recipeID string) float64 {
			return c.kb.Stats(service, recipeID).Success
		}),
		files:     c.opts.ZapretFiles,
		hostlists: c.opts.HostlistDir,
		canary:    c.canaryProbe,
		record:    func(service, recipe string, ok bool) { c.kb.RecordOutcome(service, recipe, ok, 0) },
		active:    c.zapretServices,
		resolve:   rulesets.NewResolver(c.opts.SingboxBin, c.opts.RuleSetDir, c.log).Resolve,
		log:       c.log,
	}
}

// dropUnsupportedRungs rewrites the chains so no rung names a class this host
// cannot execute. Without it the brain keeps proposing, say, a zapret rung where
// the desync is unavailable, the applier answers "no executor for class" on every
// tick, and the service sits wedged on a rung that can never be applied.
// Positions are renumbered so the surviving rungs stay contiguous.
func (c *Core) dropUnsupportedRungs(execs []executor.StrategyExecutor) {
	have := make(map[string]bool, len(execs))
	for _, e := range execs {
		have[e.Class()] = true
	}
	for name, svc := range c.reg.Services {
		kept := make([]registry.ChainStep, 0, len(svc.Chain))
		var dropped []string
		for _, step := range svc.Chain {
			if have[step.StrategyClass] {
				step.Position = len(kept)
				kept = append(kept, step)
				continue
			}
			dropped = append(dropped, step.StrategyClass)
		}
		if len(dropped) == 0 {
			continue
		}
		if len(kept) == 0 {
			// Nothing executable is left. Fall back to plain direct so traffic still
			// flows unprotected, rather than leaving a chain the brain can never apply.
			kept = []registry.ChainStep{{
				Position: 0, State: registry.StateLocked,
				StrategyClass: strategy.ClassDirect, StrategyID: "direct",
			}}
			c.log.Warn("service has no executable rung on this host — falling back to direct",
				"service", name, "dropped", dropped)
		} else {
			c.log.Info("dropped chain rungs with no executor on this host",
				"service", name, "dropped", dropped)
		}
		svc.Chain = kept
		c.reg.Services[name] = svc
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

// absDir makes a non-empty path absolute, leaving it alone if that is not
// possible — a best-effort improvement must not turn into a startup failure.
func absDir(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func randomSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
