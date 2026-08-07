package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strace-me/lotsman/client/platform/hostdns"
	"github.com/strace-me/lotsman/client/platform/netid"
	"github.com/strace-me/lotsman/client/platform/nfqws"
	"github.com/strace-me/lotsman/client/platform/rulesets"
	"github.com/strace-me/lotsman/client/platform/winws"
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
	"github.com/strace-me/lotsman/pkg/reconcile"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/state"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/zapret"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

// defaultTunExcludes keep link-local discovery out of the tunnel: a VPN that
// swallows multicast silently breaks every neighbour-discovery protocol on the
// machine, and the failure looks like anything but routing. The LAN's own unicast
// subnet(s) are added on top at generate time by localExcludeRoutes — without them
// auto_route captures the LAN and kills SSH / local connectivity.
var defaultTunExcludes = []string{
	"224.0.0.0/4",        // all IPv4 multicast: mDNS, SSDP, LocalSend, Chromecast
	"ff00::/8",           // the IPv6 equivalent
	"255.255.255.255/32", // limited broadcast
}

const (
	defaultQNum      = 200 // the router's nfqws queue number
	defaultConnbytes = 12  // desync only the first N packets of a connection

	roamPollInterval = 15 * time.Second // how often to re-fingerprint the network
	roamDebounce     = 2                // consecutive polls a new network must persist before we swap (rides out a transient blip)
)

// Options configures a client Core.
type Options struct {
	ClashListen string        // loopback Clash-API host:port (default 127.0.0.1:9090)
	Interval    time.Duration // probe + reassert interval (default 10s)
	KBFile      string        // KB persistence path ("" = in-memory)
	KBDir       string        // per-network KB dir: the store becomes <dir>/<network-id>.json, keeping each network's learning separate (overrides KBFile when set)
	ProbeProxy  string        // socks addr to probe through the tunnel ("" = probe direct)
	// CanaryGoodputKBps is the floor a recipe must sustain to count as working.
	// Reachability alone scores a frozen path as a win — TSPU's signature failure
	// completes the handshake and then stalls — so the canary pulls real volume
	// before crediting a recipe. 0 disables the stage.
	CanaryGoodputKBps  float64
	CanaryGoodputBytes int64               // bytes to pull per measurement; 0 = 64KiB
	RuleSetDir         string              // dir holding rule-set-{geosite,geoip}/*.srs ("" = generator default, i.e. the router's /etc/sing-box)
	Tun                *singbox.TunOptions // client ingress (nil = a sensible default tun)
	// TunIPv6 gives the default tun an IPv6 address as well, so auto_route installs
	// an IPv6 default route and v6 traffic is captured instead of escaping. Without
	// it a VPN-only service whose name resolves to AAAA goes out the physical link
	// unprotected — "VPN-only" becomes untrue for any host with working IPv6.
	//
	// Off by default because the failure it trades into is visible and immediate: if
	// the exit nodes cannot reach IPv6, captured v6 flows die instead of quietly
	// working. Warned about at startup either way, so the choice is at least an
	// informed one. Ignored when Tun is supplied explicitly.
	TunIPv6 bool

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
	RoamInterval  time.Duration // how often to re-fingerprint the network for roaming (0 = default 15s; only in -kb-dir mode)
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

	// HostDNS redirects the HOST's own resolver (its browser/shell) into the tun
	// while it is up, so it resolves censored names through the trusted in-tunnel
	// resolver instead of leaking to the excluded LAN resolver. System-mutating and
	// Linux+root+tun only; off by default. See client/platform/hostdns.
	HostDNS bool
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
	kbFile string // resolved KB path (KBFile, or a per-network file under KBDir); guarded by mu
	clash  *dataplane.ClashClient
	brain  *brain.Brain
	eng    *probing.Engine     // probing engine, retained so the UI can force a recheck
	events *audit.RingRecorder // in-memory brain-transition history for the UI
	secret string

	// Roaming: re-detect the network on an interval and swap the KB when it changes.
	// detect is the fingerprint source (netid.Detect; overridable in tests). roamLoop
	// is the only WRITER of the four fields below, but the rich /status reads them
	// too, so they are guarded by roamMu.
	detect       func(context.Context) netid.Network
	roamMu       sync.Mutex    // guards the network fields below against the /status reader
	currentNet   netid.Network // full fingerprint of the live network (for /status)
	currentNetID string        // network the live KB belongs to (== currentNet.Id)
	pendingNet   string        // a candidate new network, awaiting debounce confirmation
	pendingCount int           // consecutive polls that saw pendingNet

	metrics    *metrics.Collector // populated in Start; nil until then
	metricsSrv *http.Server       // non-nil only when MetricsAddr is served
	// lastCarried is the previous observation pass's byte total per rule, so the
	// activity oracle can ask whether traffic MOVED rather than whether a
	// connection merely exists. A cumulative total cannot answer that.
	carriedMu   sync.Mutex
	lastCarried map[string]int64

	obsMu   sync.Mutex       // guards obsSnap between the observe loop and /metrics
	obsSnap observe.Snapshot // last passive-observation snapshot (zero until the first pass)

	subMu   sync.Mutex                       // guards subInfo between generate() and /status
	subInfo map[string]subscription.Userinfo // quota/expiry captured at the last fetch (for /status)

	prober dataplane.Prober // shared with the autonomy loop, reused by the desync canary
	// foreignTunnels is sampled BEFORE our own tun exists. Sampling it later would
	// always find our own interface and disable the desync rung in tun mode.
	foreignTunnels []string
	zap            desyncPlatformEngine // local desync engine (nil = no desync rung on this platform)
	zapExec        *zapretExec          // the zapret executor, reconciled periodically (nil = no desync rung)

	// pristineChains keeps every service's chain exactly as the config declared it, so
	// dropUnsupportedRungs can trim from the original on every rebuild instead of
	// compounding its own earlier trims into a permanent amputation.
	pristineChains map[string][]registry.ChainStep

	// hostDNS redirects the host's own resolver into the tun (nil = off / unsupported).
	// hostResolver is the real LAN resolver captured before the redirect, pinned as the
	// sing-box `direct` DNS server so its bootstrap does not re-read the mutated
	// resolv.conf and loop. Both set once in Start before the loop; read-only after.
	hostDNS *hostdns.Manager
	// hostResolver is the host's own resolver, pinned as the tunnel's `direct` DNS
	// server. It used to be written once in Start; roaming now re-captures it, and the
	// config generator reads it, so it is guarded.
	hostMu       sync.Mutex
	hostResolver string
	// hostDNSBroken means a revert failed, so resolv.conf may still hold the sentinel:
	// keep the handle (Restore must stay reachable) but never Redirect into it again.
	hostDNSBroken bool

	// listsDrifted is set when a background rebuild changed a domain pack, so the
	// running config is a refresh behind. Surfaced in /status rather than applied on a
	// timer: re-routing under a live tunnel unasked is worse than being one apply late.
	listsDrifted atomic.Bool

	// The DNS failover loop runs OUTSIDE the autonomy loop (its own ctx + wg): it
	// applies a provider rotation via Core.Reload, and Reload waits on the loop's wg —
	// so it must not be one of the loop's goroutines, or Reload would deadlock on
	// itself. Started once in Start, stopped in Stop; survives the Reloads it triggers.
	failoverCancel context.CancelFunc
	failoverWg     sync.WaitGroup
	tunnelIPs      []string       // proxy server IPs the desync must never touch
	lastNodes      int            // nodes the last generate loaded; zero with subscriptions declared means no tunnel
	lastServers    int            // distinct server addresses those nodes sit behind
	lastPools      map[string]int // what each declared pool selected at that generate
	lastFleet      []FleetNode    // every exit as configured, with the pools that took it

	mu         sync.Mutex
	stateMu    sync.Mutex      // serialises reads of the swappable loop state (brain/reg/eng/zap/prober) against Reload
	rootCtx    context.Context // process lifetime; the autonomy loop derives from it and is rebuilt on reload
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
	// A floor low enough that a healthy path always clears it, and a pull big
	// enough to cross the ~16KB volume cliff TSPU freezes at — measuring less
	// than that would report every frozen path as healthy.
	if opts.CanaryGoodputBytes == 0 {
		opts.CanaryGoodputBytes = 64 << 10
	}
	opts.ZapretFiles = absDir(opts.ZapretFiles)
	opts.SingboxConfig = absDir(opts.SingboxConfig)
	return &Core{
		conf: conf, reg: conf.Registry, box: box, opts: opts, log: log,
		detect: netid.Detect, pristineChains: snapshotChains(conf.Registry),
	}
}

// Start generates the sing-box config, brings the box up, and launches the
// autonomy loop (brain + applier + prober). It returns once everything is
// running; call Stop to tear down.
func (c *Core) Start(ctx context.Context) error {
	if err := c.preflight(); err != nil {
		return err
	}
	c.warnIPv6Escape()
	// The process lifetime. The autonomy loop derives its own sub-context off this
	// (in buildLoop), so a config reload can cancel + rebuild just the loop while the
	// box, KB, control socket and metrics server (all process-level) stay up, and
	// SIGTERM (cancelling ctx) still stops everything.
	c.rootCtx = ctx

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
	c.resolveKBPath(ctx)
	// A rich /status wants the current network in every mode; resolveKBPath only
	// fingerprints it in per-network (KBDir) mode, so detect once here otherwise.
	// This runs before any goroutine that could also touch currentNet, so no lock.
	if c.currentNet.Id == "" {
		c.currentNet = c.detect(ctx)
	}
	if c.kbFile != "" {
		if err := c.kb.Load(c.kbFile); err != nil {
			c.log.Warn("kb load failed (starting from seed)", "path", c.kbFile, "err", err)
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

	// Host-DNS: capture the real LAN resolver BEFORE generate, so the DNS block can
	// pin its `direct` server to it (below, in singboxOptions) instead of type:local
	// — which would re-read the resolv.conf we are about to redirect and loop. The
	// redirect itself happens only after the tun is up (further down). Off unless the
	// operator asked for it, and only where it can work (Linux + root + tun).
	c.setupHostDNS()

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
			// Put the host's resolver back before anything else — a failed start must
			// not leave the machine pointed at a sentinel whose tun is gone.
			if c.hostDNS != nil {
				if err := c.hostDNS.Restore(); err != nil {
					c.log.Warn("host-dns: restore after a failed start", "err", err)
				}
			}
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

	// The tun is up and hijack-dns is live, so redirecting the host's resolver into
	// it now catches the host's own DNS instead of leaving a gap with no resolver.
	if c.hostDNS != nil {
		if err := c.hostDNS.Redirect(); err != nil {
			c.log.Warn("host-dns: could not redirect the host resolver (continuing; the host's own DNS may leak to the ISP)", "err", err)
			c.hostDNS = nil
		} else if err := c.hostDNS.Verify(ctx); err != nil {
			// The redirect installed but a lookup through the sentinel did not answer.
			// Revert so the host keeps its original (working) resolver instead of a dead
			// sentinel. Do NOT give up on the feature: this verify runs seconds after
			// boot, when DHCP and the node pool are least settled, so the likeliest cause
			// is "not ready yet", not "this host cannot hijack". Keep the handle so the
			// supervisor's healthy-tick re-assert can try again — one nervous lookup at
			// boot should not silently cost the operator host-DNS for the whole session.
			c.log.Warn("host-dns: reverting for now — the redirected resolver did not answer a test lookup; will retry", "err", err)
			if rerr := c.hostDNS.Restore(); rerr != nil {
				// The revert FAILED: resolv.conf still points at a sentinel that does not
				// answer. Dropping the handle here would remove the only way to retry the
				// restore — on stop, or when the box goes down — and leave the machine
				// with no working DNS. Keep the handle, but mark it broken so nothing
				// re-redirects into a sentinel already known not to work.
				c.log.Error("host-dns: revert FAILED — resolv.conf still points at the sentinel; will retry on stop", "err", rerr)
				c.hostDNSBroken = true
			}
			// The handle stays either way: Restore must remain reachable, and a later
			// healthy tick may well succeed where this one did not.
		} else {
			c.log.Info("host-dns: verified — the host now resolves through the tunnel")
		}
	}

	// Process-level, created once and reused across a config reload: the event ring
	// and the metrics collector. The collector reads the CURRENT brain via a closure
	// (under stateMu), so a rebuilt loop is reflected without re-creating the collector
	// or the metrics server below.
	c.events = audit.NewRingRecorder(0)
	c.metrics = metrics.New(
		func() []brain.ServiceState {
			c.stateMu.Lock()
			defer c.stateMu.Unlock()
			if c.brain == nil {
				return nil
			}
			return c.brain.Snapshot()
		},
		c.kb.Snapshot,
	)
	if c.opts.MetricsAddr != "" {
		c.metrics.SetObserveSnapshot(func() observe.Snapshot {
			c.obsMu.Lock()
			defer c.obsMu.Unlock()
			return c.obsSnap
		})
	}

	// Build and launch the autonomy loop (bus, execs, brain, applier, prober, probing
	// engine + the background runners) against the current config.
	if err := c.buildLoop(); err != nil {
		return fmt.Errorf("core: build autonomy loop: %w", err)
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
			mux.Handle("/metrics", c.metrics)
			c.metricsSrv = &http.Server{Handler: mux}
			go c.metricsSrv.Serve(ln)
			c.log.Info("metrics enabled", "addr", c.opts.MetricsAddr, "path", "/metrics")
		}
	}
	// DNS failover: probe the active remote resolver and rotate providers when it goes
	// dark. Runs outside the autonomy loop (its own ctx + wg) so the Reload it applies
	// does not deadlock on the loop's wg. Only meaningful in tun mode with a failover
	// list and a reconcilable config.
	if c.opts.ProxyListen == "" && c.opts.SingboxConfig != "" && c.conf.DNS != nil && len(c.conf.DNS.Failover) > 0 {
		fctx, fcancel := context.WithCancel(ctx)
		c.failoverCancel = fcancel
		c.failoverWg.Add(1)
		go func() { defer c.failoverWg.Done(); c.dnsFailoverLoop(fctx) }()
		c.log.Info("dns: failover armed", "providers", c.conf.DNS.Failover)
	}

	started = true
	c.log.Info("lotsman client started", "services", len(c.reg.Services), "clash", c.opts.ClashListen)
	return nil
}

// buildLoop builds and launches the autonomy loop (bus, execs, brain, applier,
// prober, probing engine + the background runners) against the CURRENT c.conf/c.reg,
// on a fresh sub-context off c.rootCtx. Start calls it once; Reload calls it again
// after swapping the config, so the loop is rebuilt while the box, KB, control socket
// and metrics server (all process-level) stay up. On the reload path the caller holds
// stateMu, so the swaps of brain/eng/prober/zap below are safe against the control
// server's readers; on the Start path there are no readers yet.
func (c *Core) buildLoop() error {
	c.mu.Lock()
	c.life, c.lifeCancel = context.WithCancel(c.rootCtx)
	runCtx, cancel := c.life, c.lifeCancel
	c.cancel = cancel
	c.mu.Unlock()

	// A fresh bus per loop — a rebuilt loop must not inherit stale in-flight verdicts.
	c.bus = events.NewBus()

	execs := []executor.StrategyExecutor{
		executor.NewVPN(c.clash, false, c.log),
		executor.NewDirect(c.clash, false, c.log),
	}
	if z := c.newZapretExec(runCtx); z != nil {
		c.zapExec = z
		execs = append(execs, z)
	}
	c.dropUnsupportedRungs(execs)

	c.brain = brain.New(c.bus, c.reg, c.kb, brain.DefaultConfig(), c.events, c.log)
	c.brain.SetReassert(c.opts.Interval)
	if c.opts.StateFile != "" {
		store := state.NewFileStore(c.opts.StateFile)
		c.brain.Restore(store.Load())
		c.brain.SetPersist(func(pos map[string]int) { store.Save(pos) })
	}
	ap := applier.New(c.bus, execs, c.log)

	specs := map[string]dataplane.ServiceProbe{}
	for name, svc := range c.reg.Services {
		specs[name] = dataplane.ServiceProbe{Type: svc.ProbeType, Target: svc.ProbeTarget}
	}
	probeVia := c.opts.ProbeProxy
	if c.opts.ProxyListen != "" {
		if probeVia != "" && probeVia != c.opts.ProxyListen {
			c.log.Warn("ignoring -probe-proxy in proxy mode; probing through -proxy instead",
				"probe_proxy", probeVia, "proxy", c.opts.ProxyListen)
		}
		probeVia = c.opts.ProxyListen
	}
	mp := dataplane.NewMultiProberProxy(specs, probeVia)
	var directMP *dataplane.MultiProber
	if c.opts.ProxyListen != "" {
		directMP = dataplane.NewMultiProberProxy(specs, "")
	}
	for name, svc := range c.reg.Services {
		for _, step := range svc.Chain {
			if step.ProbeType != "" {
				ov := dataplane.ServiceProbe{Type: step.ProbeType, Target: step.ProbeTarget}
				mp.OverrideRung(name, step.Position, ov)
				if directMP != nil {
					directMP.OverrideRung(name, step.Position, ov)
				}
			}
		}
	}
	rp := dataplane.NewRungProber(mp, c.clash, c.reg, "", 0)
	if directMP != nil {
		rp.SetDirectProber(directMP)
	}
	c.prober = rp
	eng := probing.New(c.bus, rp, c.brain, c.reg, c.kb, c.metrics, faillog.Nop{}, c.opts.Interval, c.log)
	// Let the desync canary fail an otherwise-healthy probe. The active probe pulls
	// a couple of hundred bytes, so a recipe that establishes and then carries
	// nothing reads as perfect health; the canary measures goodput and sees the
	// truth. Without this the two disagree forever and the brain believes the probe:
	// measured on the ThinkPad as youtube pinned to a 0 KB/s recipe through ninety
	// consecutive canary failures, escalating never.
	if c.zapExec != nil {
		eng.SetStallOracle(c.zapExec.StallReason)
	}
	// And the other direction: do not count a probe failure against a rule the
	// operator is visibly using right now. Discord's gateway URL timed out for
	// minutes while a voice call ran through the same rule; escalating on that
	// would have torn the call down to chase a failure the probe invented.
	eng.SetActivityOracle(c.CarryingReason)
	c.eng = eng

	runners := []func(context.Context){c.brain.Run, ap.Run, eng.Run, c.superviseBox}
	if c.opts.RefreshEvery > 0 && c.opts.SingboxConfig != "" {
		rc := c.newReconciler()
		runners = append(runners, func(rctx context.Context) { c.refreshLoop(rctx, rc, c.opts.RefreshEvery) })
	}
	runners = append(runners, c.observeLoop, c.kbDecayLoop)
	if len(c.conf.Hostlists) > 0 {
		runners = append(runners, c.hostlistLoop)
	}
	if c.zapExec != nil {
		runners = append(runners, c.desyncReconcileLoop)
	}
	if c.opts.KBDir != "" {
		runners = append(runners, c.roamLoop)
	}
	for _, run := range runners {
		c.wg.Add(1)
		go func(fn func(context.Context)) {
			defer c.wg.Done()
			fn(runCtx)
		}(run)
	}
	return nil
}

// Reload applies newConf in place WITHOUT re-executing the process: it rebuilds the
// autonomy loop against newConf and restarts sing-box ONLY if its generated config
// actually changed (an edit that does not touch routing keeps every live connection).
// The KB (learning), control socket and metrics server stay up; the desync engine is
// re-armed, which does not drop the VPN tunnel (nfqws is a separate layer on direct
// traffic). Returns an error if it cannot apply in place, so the caller falls back to
// a full re-exec. Serialised against the control server's readers by stateMu.
func (c *Core) Reload(newConf *config.Config) error {
	if c.opts.SingboxConfig == "" {
		return fmt.Errorf("core: reload needs -singbox-config to reconcile in place")
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	c.mu.Lock()
	cancel, root := c.cancel, c.rootCtx
	c.mu.Unlock()
	if cancel == nil || root == nil {
		return fmt.Errorf("core: reload before start")
	}

	// Tear the loop down; the box, KB, control socket and metrics server stay up.
	cancel()
	c.wg.Wait()

	// Stop the old desync engine — buildLoop re-arms it from newConf.
	if c.zap != nil {
		if err := c.zap.Stop(context.WithoutCancel(root)); err != nil {
			c.log.Warn("reload: stopping the old desync engine", "err", err)
		}
		c.zap, c.zapExec = nil, nil
	}

	// Adopt the new config. The KB survives, so learning is not lost.
	prevConf, prevReg := c.conf, c.reg
	c.conf = newConf
	c.reg = newConf.Registry
	c.pristineChains = snapshotChains(newConf.Registry)
	catalog := strategy.BuiltinCatalog()
	for _, d := range newConf.Strategies {
		catalog.Add(d)
	}
	c.kb.SetZapretSeed(catalog.ZapretIDs())

	// The loop is DOWN from here until buildLoop runs. Every exit past this point must
	// rebuild it, or the client keeps a live tunnel with nothing steering it: no brain,
	// no probes, no supervisor — while Healthy() still answers true from the box and
	// /status keeps replaying the frozen last snapshot. That is the worst failure this
	// client can have, because it looks healthy.
	rebuilt := false
	defer func() {
		if !rebuilt {
			if err := c.buildLoop(); err != nil {
				c.log.Error("reload: the autonomy loop could not be rebuilt — the tunnel is unsupervised", "err", err)
			}
		}
	}()

	// Reconcile the box: regenerate from newConf and restart sing-box only on a real
	// diff. A non-routing edit produces the same config → no restart → connection kept.
	switch err := c.reconcileBox(context.WithoutCancel(root), c.newReconciler()); {
	case err == nil:
		// Applied. newConf carries whatever the background refresh wrote, so the running
		// config is no longer behind.
		c.listsDrifted.Store(false)
	case errors.Is(err, reconcile.ErrDeferred), errors.Is(err, reconcile.ErrNotApplied):
		// Nothing was written: the live box still runs the PREVIOUS config, so the
		// registry must go back with it or the brain would steer selectors that the
		// running config does not contain.
		c.conf, c.reg = prevConf, prevReg
		rebuilt = true
		if berr := c.buildLoop(); berr != nil {
			return fmt.Errorf("core: reload skipped (%w) and the loop could not be rebuilt: %v", err, berr)
		}
		return fmt.Errorf("core: config saved but not applied: %w", err)
	default:
		c.conf, c.reg = prevConf, prevReg
		return fmt.Errorf("core: reload reconcile: %w", err)
	}

	c.log.Info("config reloaded in place", "services", len(c.reg.Services))
	rebuilt = true
	return c.buildLoop()
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

// roamLoop re-fingerprints the network on an interval and swaps the KB when it
// changes. It runs only in per-network mode (KBDir set).
func (c *Core) roamLoop(ctx context.Context) {
	every := c.opts.RoamInterval
	if every <= 0 {
		every = roamPollInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.maybeRoam(ctx, c.detect(ctx))
		}
	}
}

// maybeRoam decides whether net is a confirmed move to a different network and,
// if so, saves the current KB and swaps in that network's store. It reports
// whether a swap happened. A change must persist for roamDebounce consecutive
// polls to count, so a momentary disconnect (which reads as Fallback) or a brief
// flap does not thrash the store. Only roamLoop's goroutine calls this.
func (c *Core) maybeRoam(ctx context.Context, net netid.Network) bool {
	// An undetectable network (a transient disconnect) is not a move: keep the
	// current store and wait for a real network rather than swapping to the shared
	// Fallback id mid-blip. The network fields are read by /status too, so every
	// access here takes roamMu — but never across the kb IO below.
	c.roamMu.Lock()
	if net.Id == "" || net.Id == netid.Fallback || net.Id == c.currentNetID {
		c.pendingNet, c.pendingCount = "", 0
		c.roamMu.Unlock()
		return false
	}
	if net.Id == c.pendingNet {
		c.pendingCount++
	} else {
		c.pendingNet, c.pendingCount = net.Id, 1
	}
	confirmed := c.pendingCount >= roamDebounce
	c.roamMu.Unlock()
	if !confirmed {
		return false
	}

	c.mu.Lock()
	old := c.kbFile
	c.mu.Unlock()
	if err := c.kb.Save(old); err != nil {
		c.log.Warn("kb save before roam failed (learning for the old network may be lost)", "path", old, "err", err)
	}
	newPath := filepath.Join(c.opts.KBDir, net.Id+".json")
	if err := c.kb.Reload(newPath); err != nil {
		// Keep the current KB rather than run on a half-swapped one.
		c.log.Warn("kb reload on roam failed, keeping the previous store", "path", newPath, "err", err)
		c.roamMu.Lock()
		c.pendingNet, c.pendingCount = "", 0
		c.roamMu.Unlock()
		return false
	}
	c.mu.Lock()
	c.kbFile = newPath
	c.mu.Unlock()
	c.roamMu.Lock()
	c.currentNet = net
	c.currentNetID = net.Id
	c.pendingNet, c.pendingCount = "", 0
	c.roamMu.Unlock()
	c.log.Info("network changed — swapped knowledge base",
		"network", net.Id, "kind", net.Kind, "carrier", net.Carrier, "iface", net.IFace, "kb", newPath)
	c.recaptureForNetwork(ctx)
	return true
}

// recaptureForNetwork re-derives everything the generated config pinned to the OLD
// network and applies it.
//
// Two values are captured once at startup and were then carried forever: the host's own
// resolver (pinned as the tunnel's `direct` DNS server) and the machine's directly
// connected subnets (excluded from the tun so the LAN keeps working). Close the lid at
// home, open it on cafe wifi, and both are wrong: `direct` points at a router that is
// not on this network, and the cafe's own resolver is NOT excluded, so queries to it are
// swallowed by the tun and hijacked. Both DNS exits dead — while every health signal
// stays green, because the box is alive and the Clash API answers. That is the single
// most ordinary laptop event, so it must not depend on a restart to recover.
//
// A FRESH reconciler is what makes this land: the running one snapshotted its options
// when the loop was built, so it would regenerate the old excludes forever. Reconcile
// then restarts sing-box only if the regenerated config actually differs, which on a
// real network change it does.
func (c *Core) recaptureForNetwork(ctx context.Context) {
	if c.hostDNS != nil && !c.hostDNSBroken {
		// Content-based: if the system rewrote resolv.conf for the new network, this
		// re-captures it as the original before reinstalling the sentinel.
		if err := c.hostDNS.Redirect(); err != nil {
			c.log.Warn("roam: could not re-assert the host DNS redirect", "err", err)
		}
		if res := c.hostDNS.Resolvers(); len(res) > 0 {
			c.hostMu.Lock()
			changed := c.hostResolver != res[0]
			c.hostResolver = res[0]
			c.hostMu.Unlock()
			if changed {
				c.log.Info("roam: re-pinned the tunnel's direct resolver to this network's", "resolver", res[0])
			}
		}
	}
	if c.opts.SingboxConfig == "" {
		return // nothing to reconcile against
	}
	// Report what happened, not what was asked for. A deferred or not-applied
	// reconcile leaves the machine carrying the OLD network's tun excludes — which
	// is what makes it unreachable on its own LAN — and this used to log success for
	// it, because both sentinels were folded into the "no error" branch. That is the
	// failure hiding behind a green line.
	//
	// Not fatal any more, because it no longer has to be: the periodic refresh now
	// recomputes the excludes too (Reconciler.TunExcludes), so a missed recapture is
	// repaired within a refresh interval instead of lasting until a restart.
	switch err := c.reconcileBox(ctx, c.newReconciler()); {
	case err == nil:
		c.log.Info("roam: config regenerated for the new network")
	case errors.Is(err, reconcile.ErrDeferred), errors.Is(err, reconcile.ErrNotApplied):
		c.log.Warn("roam: the new network's config was NOT applied yet — the tun still excludes the old network's subnets, so this machine may be unreachable on its LAN until the next refresh repairs it",
			"reason", err)
	default:
		c.log.Warn("roam: could not regenerate the config for the new network", "err", err)
	}
}

// kbDecayInterval and kbDecayFactor mirror the daemon's: 0.95 every 15 minutes, a
// ~3.4h half-life on observation counts.
const (
	kbDecayInterval = 15 * time.Minute
	kbDecayFactor   = 0.95
)

// kbDecayLoop ages the knowledge base the way the daemon does, and — the part that
// matters on a laptop — counts down the circuit breaker's quarantines.
//
// Only the router ever called Decay, so on the client a quarantine was PERMANENT: three
// consecutive canary failures banished a (service, recipe) pair for the life of the
// process. That is a lot on a machine that suspends, because every resume spends its
// first half-minute with no route, every probe in that window fails, and the recipes
// being blamed had nothing to do with it. Left alone, a few weeks of lid-opens quietly
// quarantine every recipe that works on the home network — the tool slowly forgets what
// it learned, and nothing says so.
//
// Decaying also lifts the exploration bonus on strategies nobody has retried lately, so
// "worked before" gets re-validated instead of trusted forever.
func (c *Core) kbDecayLoop(ctx context.Context) {
	t := time.NewTicker(kbDecayInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.kb.Decay(kbDecayFactor)
		}
	}
}

// observeLoop refreshes the passive eye's snapshot each interval, feeding both the
// rich /status (always) and the /metrics collector (when served). It never touches
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

// resolveKBPath decides where this run's knowledge base lives. With KBDir set the
// store is per-network — <dir>/<network-id>.json — so a strategy learned behind
// one network's DPI is not carried onto another, and returning to a known network
// reuses what was already learned there instead of starting cold. Detection is at
// startup only for now; roaming mid-run is a later step. KBFile (a single shared
// store) remains the default when KBDir is unset.
func (c *Core) resolveKBPath(ctx context.Context) {
	if c.opts.KBDir == "" {
		c.kbFile = c.opts.KBFile
		return
	}
	net := netid.Detect(ctx)
	if err := os.MkdirAll(c.opts.KBDir, 0o700); err != nil {
		c.log.Warn("per-network KB dir unavailable, falling back to shared store", "dir", c.opts.KBDir, "err", err)
		c.kbFile = c.opts.KBFile
		return
	}
	c.kbFile = filepath.Join(c.opts.KBDir, net.Id+".json")
	c.currentNet = net
	c.currentNetID = net.Id
	c.log.Info("per-network knowledge base",
		"network", net.Id, "gateway", net.Gateway, "iface", net.IFace,
		"mac_known", net.MAC != "", "kind", net.Kind, "carrier", net.Carrier, "kb", c.kbFile)
}

// Stop cancels the autonomy loop, stops sing-box, and persists the KB.
func (c *Core) Stop() error {
	// Stop the DNS failover loop first so it cannot begin a Reload mid-shutdown; wait
	// for any Reload it already started to finish before tearing the loop down.
	if c.failoverCancel != nil {
		c.failoverCancel()
		c.failoverWg.Wait()
		c.failoverCancel = nil
	}

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
	// Put the host's resolver back before tearing the tun down, so the machine is
	// never left pointing at an in-tun sentinel whose tun no longer exists.
	if c.hostDNS != nil {
		if err := c.hostDNS.Restore(); err != nil {
			c.log.Warn("host-dns: restore on stop", "err", err)
		}
	}
	_ = c.box.Stop(context.Background())
	if c.kbFile != "" && c.kb != nil {
		if err := c.kb.Save(c.kbFile); err != nil {
			c.log.Warn("kb save failed", "path", c.kbFile, "err", err)
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

	// Enriched for the rich /status (all additive — a consumer decoding only the
	// original five fields is unaffected).
	Rung      int    `json:"rung"`                // chain position the brain settled on
	RungClass string `json:"rungClass,omitempty"` // zapret | vpn | direct | emergency
	// Engine is the PROCESS carrying this service right now — nfqws for a desync
	// rung, sing-box for everything routed. The rung class alone does not say it,
	// and "which engine is this rule living in" is the first thing you need when a
	// service misbehaves: it decides whether you look at desync recipes or at nodes.
	Engine string `json:"engine,omitempty"`
	// Strategy is what is ACTUALLY running: the composed desync recipe, or the pool
	// a routed service selects through.
	Strategy string `json:"strategy,omitempty"`
	// Requested is what the brain ASKED for, when that differs from Strategy. They
	// diverge silently today — the brain can name a strategy the local engine cannot
	// render, and the executor quietly falls back to the best recipe it can build —
	// so a UI showing only one of the two describes a machine that does not exist.
	Requested    string  `json:"requested,omitempty"`
	StalledRatio float64 `json:"stalledRatio,omitempty"` // passive-eye freeze ratio (0 when the eye saw nothing)
	LeakRatio    float64 `json:"leakRatio,omitempty"`    // passive-eye leak-to-direct ratio
}

// Status reports each service's brain position plus the concrete node its selector
// currently points at (read over the Clash-API), under stateMu so a concurrent
// config reload cannot swap the brain/reg mid-read.
func (c *Core) Status(ctx context.Context) []NodeStatus {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.statusLocked(ctx)
}

// statusLocked is Status's body; the caller holds stateMu (Status, or Report).
func (c *Core) statusLocked(ctx context.Context) []NodeStatus {
	if c.brain == nil {
		return nil
	}
	obs := c.observeSnapshot()
	var out []NodeStatus
	for _, s := range c.brain.Snapshot() {
		node := ""
		if info, err := c.clash.Proxy(ctx, registry.SelectorTag(s.Service)); err == nil {
			node = info.Now
		}
		ns := NodeStatus{
			Service: s.Service, State: s.State, Node: node,
			Fails: s.Fails, Broken: s.Broken, Rung: s.Position,
		}
		// Join the brain position with the registry chain for the rung's class and
		// strategy. A zapret rung carries no static StrategyID (it is resolved from the
		// KB at escalation), so fill the live recipe the desync engine actually applied.
		if svc, ok := c.reg.Services[s.Service]; ok && s.Position >= 0 && s.Position < len(svc.Chain) {
			step := svc.Chain[s.Position]
			ns.RungClass = step.StrategyClass
			ns.Strategy = step.StrategyID
			if step.StrategyClass == strategy.ClassZapret {
				if c.zapExec != nil {
					ns.Engine = "nfqws"
					// What the desync engine ACTUALLY composed. It can differ from what
					// the brain resolved, because the KB ranks strategy ids while the
					// engine can only render recipes it holds.
					ns.Strategy = c.zapExec.chosenRecipe(s.Service)
				}
			} else {
				ns.Engine = "sing-box"
			}
			// Say what was asked for only when it is not what is running — a UI that
			// always printed both would make the ordinary case look like a discrepancy.
			if s.Strategy != "" && s.Strategy != ns.Strategy {
				ns.Requested = s.Strategy
			}
		}
		if m, ok := obs.Services[s.Service]; ok {
			ns.StalledRatio = m.StalledRatio
			ns.LeakRatio = m.LeakRatio
		}
		out = append(out, ns)
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

// Recheck forces an immediate probe of one service (the UI's "recheck now"). It
// returns before the probe completes; the verdict lands on the brain through the
// normal bus. eng is checked first so a request before Start cannot nil-deref reg.
func (c *Core) Recheck(service string) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.eng == nil {
		return fmt.Errorf("core: probing engine not running")
	}
	if _, ok := c.reg.Services[service]; !ok {
		return fmt.Errorf("core: unknown service %q", service)
	}
	c.eng.ProbeNow(service)
	return nil
}

// Events returns recent brain rung-transitions newest-first (the UI's "история
// событий"), optionally filtered to one service; limit <= 0 returns all retained.
func (c *Core) Events(limit int, service string) []audit.Transition {
	if c.events == nil {
		return nil
	}
	return c.events.Snapshot(limit, service)
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
	// Config knobs the daemon has always honoured. Without these the client silently
	// dropped them: a config could ask for a uTLS fingerprint or multiplex and get
	// neither, with nothing in the logs — and those two are exactly the levers against
	// TLS-fingerprinting and the parallel-handshake/session-count heuristics.
	opts.UTLSFingerprint = c.conf.UTLSFingerprint
	if c.conf.SingboxVersion != "" {
		// Gates version-specific knobs (e.g. the 1.12+ DNS block) to what the installed
		// binary can actually load, instead of assuming the baseline.
		opts.TargetVersion = c.conf.SingboxVersion
	}
	// A disabled block parses to nil, so non-nil already means "enabled".
	if c.conf.FakeIP != nil {
		opts.FakeIP = &singbox.FakeIPOptions{
			Inet4Range: c.conf.FakeIP.Inet4Range,
			Inet6Range: c.conf.FakeIP.Inet6Range,
			Resolver:   c.conf.FakeIP.Resolver,
		}
	}
	if c.conf.Multiplex != nil {
		opts.Multiplex = &singbox.MultiplexOptions{
			Protocol:       c.conf.Multiplex.Protocol,
			MaxConnections: c.conf.Multiplex.MaxConnections,
			MinStreams:     c.conf.Multiplex.MinStreams,
			Padding:        c.conf.Multiplex.Padding,
			BrutalUp:       c.conf.Multiplex.BrutalUp,
			BrutalDown:     c.conf.Multiplex.BrutalDown,
		}
	}
	// NOT passed through: subscription_via_pool. The router needs it because tproxy
	// only catches LAN traffic, so the box's own subscription fetch would go direct;
	// a tun client's own traffic is already captured by auto_route. Wiring it for
	// proxy mode would also mean swapping the subscription loader onto the proxy —
	// a separate change, not a silent half-measure here.
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
		// A tun captures DNS system-wide, so the client must carry its own: without a
		// dns block sing-box has no resolver and lookups fail. Proxy mode doesn't
		// capture DNS, so it keeps the system resolver (no block). A user-declared
		// dns config overrides the built-in split-DNS default.
		if c.conf.DNS != nil {
			opts.DNS = dnsFromConfig(c.conf.DNS, "vpn_url_test")
		} else {
			opts.DNS = defaultDNS("vpn_url_test")
		}
		// When host-DNS has redirected the system resolv.conf into the tun, the
		// `direct` server can no longer be type:local (it would re-read the redirected
		// file and loop); pin it to the real LAN resolver captured before the redirect.
		// Gate on the feature actually being live (hostDNS non-nil), not just a captured
		// resolver — a failed verify nils hostDNS but leaves hostResolver set.
		c.hostMu.Lock()
		resolver := c.hostResolver
		c.hostMu.Unlock()
		if c.hostDNS != nil && resolver != "" {
			pinDirectResolver(opts.DNS, resolver)
		}
		if c.opts.ProbeProxy != "" {
			opts.SocksProbeListen = c.opts.ProbeProxy
		}
	}

	return opts
}

// setupHostDNS builds the host-DNS redirector and captures the real LAN resolver,
// so singboxOptions can pin the sing-box `direct` server to it. It is a no-op
// unless -host-dns is set and the mode can support it (Linux + root + tun): the
// redirect rewrites /etc/resolv.conf, which needs root, and only makes sense when a
// tun is actually capturing DNS. A capture failure disables the feature rather than
// blocking startup — the client is still useful without host-DNS.
func (c *Core) setupHostDNS() {
	if !c.opts.HostDNS || c.opts.ProxyListen != "" {
		return
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		c.log.Warn("host-dns unavailable: it rewrites /etc/resolv.conf, so it needs Linux + root + tun mode",
			"goos", runtime.GOOS, "euid", os.Geteuid())
		return
	}
	sentinel := tunSentinel(c.tunOptions())
	if sentinel == "" {
		c.log.Warn("host-dns disabled: no tun gateway address to point the host at")
		return
	}
	m := hostdns.New(sentinel, "/run/lotsman/resolv.conf.orig", "", c.log)
	res, err := m.Capture()
	if err != nil {
		c.log.Warn("host-dns disabled: could not read the host resolver", "err", err)
		return
	}
	if len(res) == 0 {
		// Without a real resolver to pin the sing-box `direct` server to, redirecting
		// resolv.conf into the tun would make sing-box's own bootstrap re-read the
		// sentinel and loop. Refuse rather than break the tunnel's DNS.
		c.log.Warn("host-dns disabled: no usable IPv4 resolver to pin the bootstrap to (a redirect would loop sing-box's own DNS)")
		return
	}
	c.hostDNS = m
	// No contention here (Start runs before any loop goroutine), but the field is
	// mutable now that roaming re-captures it, so take the lock everywhere.
	c.hostMu.Lock()
	c.hostResolver = res[0]
	c.hostMu.Unlock()
}

// tunSentinel is the in-tun address the host's resolver is pointed at. It is the
// tun's PEER address, NOT the tun's own interface address: with stack:system a
// packet to the interface's own address is delivered locally and never reaches
// sing-box's tun reader to be hijacked, whereas the peer routes INTO the tun. In
// the /30 the client uses (172.19.0.1/30) that peer is 172.19.0.2. A verify step
// after the redirect catches any host where even this does not get hijacked.
func tunSentinel(t *singbox.TunOptions) string {
	if t == nil || len(t.Address) == 0 {
		return ""
	}
	ip, ipnet, err := net.ParseCIDR(t.Address[0])
	if err != nil {
		return ""
	}
	ip = ip.To4()
	if ip == nil {
		return ""
	}
	peer := make(net.IP, len(ip))
	copy(peer, ip)
	peer[len(peer)-1]++
	if !ipnet.Contains(peer) {
		copy(peer, ip)
		peer[len(peer)-1]--
	}
	if peer.Equal(ip) || !ipnet.Contains(peer) {
		return ""
	}
	return peer.String()
}

// pinDirectResolver rewrites the DNS block's `direct` server to dial a concrete
// resolver IP over plain UDP, instead of `type: local` reading the system
// resolv.conf. It is used only when host-DNS has redirected that resolv.conf into
// the tun: sing-box's own bootstrap must keep reaching the REAL LAN resolver (its
// traffic bypasses its tun via the routing mark), or resolving the DoH hostname
// would loop back through the sentinel. A no-op if the direct server can't be found.
func pinDirectResolver(dns *singbox.DNSOptions, resolver string) {
	if dns == nil || resolver == "" || dns.Direct == "" {
		return
	}
	for i := range dns.Servers {
		if dns.Servers[i].Tag == dns.Direct {
			dns.Servers[i] = singbox.DNSServer{Tag: dns.Direct, Type: "udp", Server: resolver}
			return
		}
	}
}

// defaultDNS is the split-DNS a tun client runs with when the config declares none:
// censored/default domains resolve via Cloudflare DoH DETOURED THROUGH THE VPN pool
// (so the ISP neither sees the queries in the clear nor poisons the answers), while
// the OS resolver bootstraps that DoH hostname and serves the RU-direct rule-sets.
// The pool detour is dropped by the generator when the pool doesn't exist yet (no
// nodes), degrading to DoH-over-direct — still encrypted, just not tunnelled.
func defaultDNS(pool string) *singbox.DNSOptions {
	return &singbox.DNSOptions{
		Servers: []singbox.DNSServer{
			{Tag: "dns_remote", Type: "https", Server: "cloudflare-dns.com", ServerName: "cloudflare-dns.com", Bootstrap: "dns_direct", Detour: pool},
			{Tag: "dns_direct", Type: "local"},
		},
		Direct: "dns_direct",
		Final:  "dns_remote",
	}
}

// dnsFromConfig maps a user-declared config.DNS to generator options, resolving the
// "vpn" detour alias to the primary VPN pool tag and pointing a hostname-addressed
// server's bootstrap at the direct resolver.
func dnsFromConfig(d *config.DNS, pool string) *singbox.DNSOptions {
	out := &singbox.DNSOptions{Direct: d.Direct, Final: d.Final, Strategy: d.Strategy}
	if d.FakeIP {
		out.FakeIP = &singbox.FakeIPOptions{} // standard reserved ranges
	}
	for _, s := range d.Servers {
		detour := s.Detour
		if detour == "vpn" {
			detour = pool
		}
		srv := singbox.DNSServer{
			Tag: s.Name, Type: s.Type, Server: s.Address, Port: s.Port,
			Path: s.Path, ServerName: s.ServerName, Detour: detour,
		}
		if s.Bootstrap && d.Direct != "" {
			srv.Bootstrap = d.Direct
		}
		out.Servers = append(out.Servers, srv)
	}
	return out
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
	// Capture the quota/expiry the fetch just parsed from the Subscription-Userinfo
	// header before mgr is discarded, so /status can report it (the mgr itself is
	// throwaway — only this snapshot is retained).
	c.subMu.Lock()
	c.subInfo = mgr.Userinfo()
	c.subMu.Unlock()
	memberships := c.conf.Pools.Memberships(nodes)
	c.tunnelIPs = tunnelIPs(nodes, c.log)
	c.lastNodes = len(nodes)
	// Record the other two counts while we hold the material for them: how many
	// distinct addresses the fleet actually sits behind, and what each pool
	// selected. A single "nodes" number cannot answer "why does this service only
	// have three to choose from".
	servers := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		servers[n.Server+":"+strconv.Itoa(n.Port)] = true
	}
	c.lastServers = len(servers)
	c.lastPools = make(map[string]int, len(memberships))
	inPools := make(map[string][]string, len(nodes))
	warm := make(map[string]bool, len(nodes))
	for name, members := range memberships {
		c.lastPools[name] = len(members)
		hot := c.conf.Pools.Pools[name].Warmup
		for _, n := range members {
			inPools[n.ID] = append(inPools[n.ID], name)
			warm[n.ID] = warm[n.ID] || hot
		}
	}
	c.lastFleet = make([]FleetNode, 0, len(nodes))
	for _, n := range nodes {
		pl := inPools[n.ID]
		sort.Strings(pl)
		c.lastFleet = append(c.lastFleet, FleetNode{
			Name: n.DisplayName, Server: n.Server, Protocol: n.Protocol,
			Country: n.Country, Source: n.Source, Pools: pl, Warm: warm[n.ID],
		})
	}

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
	addr := []string{"172.19.0.1/30"}
	if c.opts.TunIPv6 {
		// A ULA /126, the v6 counterpart of the /30 above: its only job is to make
		// auto_route install a v6 default so nothing escapes the tunnel by address
		// family.
		addr = append(addr, tunIPv6Address)
	}
	return &singbox.TunOptions{
		MTU: 9000, Address: addr, Stack: "system", AutoRoute: true,
		ExcludeRoutes: tunExcludes(),
	}
}

// tunIPv6Address is the tun's IPv6 side when -tun-ipv6 is on. fd00::/8 is the
// unique-local range, so it cannot collide with anything routable.
const tunIPv6Address = "fdfe:dcba:9876::1/126"

// tunExcludes is the full route_exclude_address set: the static multicast/broadcast
// ranges plus whatever subnets this machine is on RIGHT NOW. It is a function rather
// than a value because the second half changes under a laptop — see
// reconcile.Reconciler.TunExcludes, which calls it on every reconcile.
func tunExcludes() []string {
	return append(append([]string{}, defaultTunExcludes...), localExcludeRoutes()...)
}

// localExcludeRoutes returns the machine's own directly-connected subnets so
// auto_route never pulls them into the tunnel. The ip_is_private route rule only
// steers traffic sing-box already handles, whereas auto_route decides what the kernel
// diverts into the tun in the first place — so without excluding the LAN here the tun
// swallows local traffic and kills SSH / LAN connectivity. Best-effort: on error only
// the static multicast excludes apply.
func localExcludeRoutes() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ifc := range ifaces {
		// Only real, up LAN interfaces carry a subnet to keep local. Skip loopback,
		// down, point-to-point and tun/tap/wg links: a tunnel's own /30 must never be
		// excluded or it would blackhole the tunnel itself.
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 ||
			ifc.Flags&net.FlagPointToPoint != 0 || isTunName(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			network := (&net.IPNet{IP: ipnet.IP.Mask(ipnet.Mask), Mask: ipnet.Mask}).String()
			if !seen[network] {
				seen[network] = true
				out = append(out, network)
			}
		}
	}
	sort.Strings(out) // deterministic order for the anti-churn baseline
	return out
}

// isTunName reports whether an interface name looks like a tunnel/tap/wireguard
// device, whose point-to-point subnet must not be mistaken for a LAN to exclude.
func isTunName(name string) bool {
	for _, p := range []string{"tun", "utun", "sing", "tap", "wg"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
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

// recipeScore ranks a desync recipe for a service, blending the service's own KB
// history with a per-network prior — how the SAME recipe has done for OTHER
// services on this network. The winning desync is largely a property of the
// network's DPI (two services here shared a winner), so a service meeting the
// rung for the first time should try what already worked on this network before
// falling back to catalog order, and steer clear of what already failed here.
func (c *Core) recipeScore(service, recipeID string) float64 {
	own := c.kb.Stats(service, recipeID)
	prior, priorN := c.kb.NetworkPrior(recipeID, service)
	return blendRecipePrior(own.Success, own.Count, prior, priorN)
}

// blendRecipePrior shrinks the service's own success rate toward the network
// prior by how little of its own evidence it has: a cold service (ownN≈0) leans
// entirely on the prior, and its own signal takes over as outcomes accumulate.
// With no prior (no other service tried this recipe) the own score is returned
// unchanged, so behaviour is identical to before on a fresh network.
func blendRecipePrior(ownSuccess, ownN, prior, priorN float64) float64 {
	if priorN <= 0 {
		return ownSuccess
	}
	const k = 3.0 // own observations needed to weigh equally with the prior
	w := ownN / (ownN + k)
	return w*ownSuccess + (1-w)*prior
}

// newZapretExec builds the zapret-class executor when this platform and config can
// actually support local desync. NFQUEUE is Linux-only, and there is no point
// wiring a rung no service declares. Returning nil simply means the brain never
// offers that rung — it escalates straight to VPN instead.
func (c *Core) newZapretExec(ctx context.Context) *zapretExec {
	if !c.hasZapretStep() {
		return nil
	}
	// The capture spec is the same on both platforms — the same ports carry the
	// same censored protocols — but how traffic REACHES the engine is not: Linux
	// queues it with an nft rule, Windows hands winws its own WinDivert filter.
	inst := zapret.Instance{
		Name:      "lotsman",
		QNum:      c.qnum(),
		Capture:   zapret.Capture{TCP: []string{"80", "443", "2053", "2083", "2087", "2096", "8443"}, UDP: []string{"443"}},
		Connbytes: defaultConnbytes,
	}
	switch runtime.GOOS {
	case "linux":
		if c.zap = c.newNfqwsEngine(ctx, inst); c.zap == nil {
			return nil
		}
	case "windows":
		if c.zap = c.newWinwsEngine(inst); c.zap == nil {
			return nil
		}
	default:
		c.log.Warn("desync rung unavailable: no desync engine for this platform", "goos", runtime.GOOS)
		return nil
	}

	return &zapretExec{
		clash:  c.clash,
		engine: c.zap,
		recipes: usablePayloadRecipes(
			append(strategycat.Load(), zaptune.RecipesFromDefinitions(c.conf.Strategies)...),
			c.opts.ZapretFiles, c.log),
		// Rank recipes by what has actually worked for this service. A cold KB scores
		// every candidate at the prior, so this degrades to catalog order — the same
		// choice the seed picker makes — and sharpens only as outcomes accumulate.
		pick:      zaptune.KBPicker(c.recipeScore),
		files:     c.opts.ZapretFiles,
		hostlists: c.opts.HostlistDir,
		canary:    c.canaryProbe,
		record:    func(service, recipe string, ok bool) { c.kb.RecordOutcome(service, recipe, ok, 0) },
		active:    c.zapretServices,
		resolve:   rulesets.NewResolver(c.opts.SingboxBin, c.opts.RuleSetDir, c.log).Resolve,
		log:       c.log,
	}
}

func (c *Core) qnum() int {
	if c.opts.QNum == 0 {
		return defaultQNum
	}
	return c.opts.QNum
}

// newWinwsEngine builds the Windows desync engine. winws carries its own capture
// filter, so unlike the Linux path there is no interface to detect and no ruleset
// to install — the whole platform difference is that argv.
//
// UNPROVEN: no part of the Windows desync path has run on a Windows machine.
func (c *Core) newWinwsEngine(inst zapret.Instance) desyncPlatformEngine {
	// The foreign-tunnel guard cannot be trusted here and must say so. It matches
	// interface names against Linux/BSD conventions (tun, wg, utun…), and Windows
	// names its adapters nothing like that — so a WireGuard or OpenVPN tunnel on
	// this host would very likely go unnoticed and get its packets mangled. A
	// safety check that quietly stops checking is worse than one that refuses, and
	// the only honest thing available until someone can read real adapter names off
	// a real box is to name the gap.
	if len(c.foreignTunnels) > 0 && !c.opts.DesyncForce {
		c.log.Warn("desync rung DISABLED: another tunnel is on this host and winws would mangle it",
			"interfaces", c.foreignTunnels)
		return nil
	}
	c.log.Warn("the foreign-tunnel check is Linux-shaped and cannot vouch for this host",
		"detail", "it matches adapter names like tun/wg/utun; if another VPN is running here, winws may mangle it")
	e := winws.New(c.opts.NfqwsBin, inst, c.opts.ZapretFiles, c.log)
	c.log.Info("desync rung enabled", "engine", "winws", "capture_tcp", inst.Capture.TCP, "capture_udp", inst.Capture.UDP)
	return e
}

// newNfqwsEngine builds the Linux desync engine: an nft NFQUEUE ruleset plus a
// managed nfqws child.
func (c *Core) newNfqwsEngine(ctx context.Context, inst zapret.Instance) desyncPlatformEngine {
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

	// Never desync a tunnel: ours (derived from the live node set) or a foreign
	// one the operator named.
	excluded := append(append([]string{}, c.tunnelIPs...), c.opts.DesyncExclude...)
	// ZapretFiles is nfqws's working dir: the availability check already skips recipes
	// whose payloads aren't there, and setting it as CWD is what actually lets nfqws
	// FIND them — a bare --dpi-desync-fake-tls=tls_clienthello_*.bin resolves relative
	// to CWD, so without this nfqws exits immediately even for a payload we vetted.
	e := nfqws.New(c.opts.NfqwsBin, inst, zapret.NftOptions{
		Table: "inet lotsman", WAN: wan, VPNServers: excluded,
	}, c.opts.ZapretFiles, c.log)
	// Give the engine somewhere durable to leave its last words. An engine that dies
	// on its own is the one failure the desync rung cannot see — nft keeps the queue
	// rules with `flags bypass`, so traffic keeps flowing undesynced and everything
	// downstream still reads healthy. Reuse whichever persistent dir the operator
	// already named rather than inventing a knob; with none, the exit is only logged.
	if dir := c.opts.HostlistDir; dir != "" {
		e.SetCrashLog(filepath.Join(dir, "nfqws-crash.log"))
	} else if c.opts.StateFile != "" {
		e.SetCrashLog(filepath.Join(filepath.Dir(c.opts.StateFile), "nfqws-crash.log"))
	}
	c.log.Info("desync rung enabled", "engine", "nfqws", "wan", wan, "qnum", inst.QNum, "excluded_tunnels", len(excluded))
	return e
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
		// Always trim from the PRISTINE chain, never from whatever the last pass left
		// behind. Writing the trimmed chain back into the shared registry made the loss
		// permanent: a rung dropped once because its executor happened to be
		// unavailable — the desync engine gives up when there is no default route, which
		// is exactly the state a laptop is in for the first seconds after a resume — was
		// gone from the registry, so no later rebuild could ever bring it back. The
		// desync stayed amputated until the process was restarted, and since the process
		// does not exit, Restart=on-failure never fired either.
		if orig, ok := c.pristineChains[name]; ok {
			svc.Chain = orig
		}
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
			// Nothing to drop — but still write back, because the chain we just rebuilt
			// came from the pristine copy and may be RESTORING rungs a previous pass
			// trimmed. Skipping the write here was what made the restore never land.
			svc.Chain = kept
			c.reg.Services[name] = svc
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

// snapshotChains records the chains as declared, before any host-capability trimming.
func snapshotChains(reg *registry.Registry) map[string][]registry.ChainStep {
	out := make(map[string][]registry.ChainStep, len(reg.Services))
	for name, svc := range reg.Services {
		out[name] = append([]registry.ChainStep(nil), svc.Chain...)
	}
	return out
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
