// Command lotsmanctl is the operator CLI. Today it has one job: generate a
// sing-box config from the declarative config + live subscriptions, so the
// generated file can be validated on the router with `sing-box check -c`.
//
//	lotsmanctl generate -config examples/config.yaml -out /tmp/sing-box.json
//
// Then on the R5S:  sing-box check -c /tmp/sing-box.json
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"time"

	"github.com/strace-me/lotsman/pkg/blockcheck"
	"github.com/strace-me/lotsman/pkg/coherence"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/domainscan"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/subscription"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "generate":
		generate(os.Args[2:])
	case "merge":
		merge(os.Args[2:])
	case "harvest":
		harvest(os.Args[2:])
	case "doctor":
		doctor(os.Args[2:])
	case "scan":
		scan(os.Args[2:])
	case "status":
		status(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  lotsmanctl generate -config <path> [-out <path>]")
	fmt.Fprintln(os.Stderr, "  lotsmanctl merge -config <path> -singbox-config <existing.json> [-out <path>] [-selector vpn]")
	fmt.Fprintln(os.Stderr, "  lotsmanctl harvest -script <blockcheck.sh> [-domains a,b] [-scanlevel force]")
	fmt.Fprintln(os.Stderr, "  lotsmanctl doctor -config <path> [-nfqws-exclude <list-exclude.txt>]")
	fmt.Fprintln(os.Stderr, "  lotsmanctl scan -config <path> [-timeout 6s] [-concurrency 8]   (run ON the router)")
	fmt.Fprintln(os.Stderr, "  lotsmanctl status [-metrics-addr 127.0.0.1:9101] [-clash-base http://127.0.0.1:9090]   (live daemon)")
}

// scan probes each service's inline domains over this host's own path (which
// traverses the live nfqws desync) and reports which are reachable vs blocked —
// "from the router, what isn't working". TCP (HTTPS) for every service; QUIC
// (HTTP/3) additionally for streaming-profile services, where it is a real H3
// signal (voice is UDP/RTC — a separate STUN concern, not covered here). Read-only.
func scan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	configPath := fs.String("config", "", "path to YAML config (required)")
	timeout := fs.Duration("timeout", 6*time.Second, "per-domain probe timeout")
	conc := fs.Int("concurrency", 8, "concurrent probes")
	fs.Parse(args)
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "scan: -config is required")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	tcp := domainscan.HTTPProbe(*timeout)
	quic := domainscan.HTTP3Probe(*timeout)
	ctx := context.Background()

	fmt.Println("== Lotsman per-domain reachability scan (over this host's path) ==")
	for _, s := range cfg.Registry.Services {
		if len(s.Domains) == 0 {
			continue // rule_set (.srs) domains aren't enumerable here; scan inline domains
		}
		quicRelevant := s.Profile == "streaming"
		suffix := " · TCP"
		if quicRelevant {
			suffix = " · TCP+QUIC"
		}
		fmt.Printf("\n%s  (%d domains%s)\n", s.Name, len(s.Domains), suffix)
		scanReport("TCP ", domainscan.ProbeAll(ctx, s.Domains, tcp, *conc))
		if quicRelevant {
			scanReport("QUIC", domainscan.ProbeAll(ctx, s.Domains, quic, *conc))
		}
		if s.Profile == "voice" {
			fmt.Println("  note: voice (RTC) is UDP — check it with the STUN/voicetune path, not this scan")
		}
	}
}

func scanReport(label string, res []domainscan.Result) {
	ok, blocked, na := domainscan.Tally(res)
	if blocked == 0 {
		fmt.Printf("  %s: %d ok, %d n/a (cert/dns) — no real blocks ✓\n", label, ok, na)
		return
	}
	fmt.Printf("  %s: %d ok, %d n/a, %d BLOCKED:\n", label, ok, na, blocked)
	for _, b := range domainscan.Blocked(res) {
		e := b.Err
		if len(e) > 50 {
			e = e[:50]
		}
		fmt.Printf("      ✗ %-34s %s\n", b.Domain, e)
	}
}

// doctor reports coherence between the sing-box routing model and the nfqws
// desync model: which domains zapret-routed services feed to the nfqws hostlist,
// and the gaps nfqws cannot cover (rule_set/.srs matches, ip_cidr matches, and
// domains the nfqws exclude list would drop). Read-only.
func doctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "path to YAML config (required)")
	excludePath := fs.String("nfqws-exclude", "", "optional nfqws exclude list (plaintext domains) to detect overlaps")
	fs.Parse(args)
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "doctor: -config is required")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	facts := coherence.EngineFacts{NfqwsExcludeDomains: map[string]bool{}}
	if *excludePath != "" {
		b, err := os.ReadFile(*excludePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read nfqws-exclude: %v\n", err)
			os.Exit(1)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.ToLower(strings.TrimSpace(line))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			facts.NfqwsExcludeDomains[line] = true
		}
	}

	services := make([]registry.Service, 0, len(cfg.Registry.Services))
	for _, s := range cfg.Registry.Services {
		services = append(services, s)
	}
	plan := coherence.Analyze(services, facts)

	fmt.Println("== Lotsman coherence report ==")
	fmt.Printf("\nzapret-routed services feed %d domain(s) into the nfqws hostlist:\n", len(plan.NfqwsDomains))
	for _, d := range plan.NfqwsDomains {
		fmt.Printf("  + %s\n", d)
	}
	if len(plan.Gaps) == 0 {
		fmt.Println("\nno coherence gaps ✓ — every zapret-routed match is a plaintext domain nfqws can see.")
		return
	}
	fmt.Printf("\n%d coherence gap(s) — routed to zapret, but nfqws cannot fully desync these:\n", len(plan.Gaps))
	for _, g := range plan.Gaps {
		fmt.Printf("  ! [%-15s] %s: %s\n", g.Kind, g.Service, g.Detail)
	}
	fmt.Println("\nlegend:")
	fmt.Println("  ruleset_zapret  matched by .srs (binary) — nfqws can't read it; add plaintext domains or expect no desync")
	fmt.Println("  ip_zapret       matched by ip_cidr — nfqws matches SNI not IP; needs a separate nfqws L7/port rule")
	fmt.Println("  exclude_overlap domain is in the nfqws exclude list — it will be skipped, never desynced")
}

// merge fetches the config's subscriptions, groups nodes into one url-test pool
// per subscription, and surgically injects them into an EXISTING sing-box config
// (the router's hand-tuned one), pointing the selector at the new pools. Unlike
// generate, it preserves everything already in the config (inbounds, route, DNS,
// manually-added hy2 nodes). Validate the result with `sing-box check` before use.
func merge(args []string) {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	configPath := fs.String("config", "", "path to lotsman YAML config (subscriptions) (required)")
	sbPath := fs.String("singbox-config", "", "path to existing sing-box config.json to merge into (required)")
	out := fs.String("out", "", "output path (default: stdout)")
	selector := fs.String("selector", "vpn", "selector tag to point at the new pools")
	probeURL := fs.String("probe-url", "https://www.gstatic.com/generate_204", "url-test health-check URL")
	interval := fs.String("interval", "5m", "url-test interval")
	udpMode := fs.String("udp-mode", singbox.UDPPrimaryFallback, "UDP handling: unified | split | primary-fallback")
	_ = fs.Parse(args)

	if *configPath == "" || *sbPath == "" {
		fmt.Fprintln(os.Stderr, "merge: -config and -singbox-config are required")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: config: %v\n", err)
		os.Exit(1)
	}
	existing, err := os.ReadFile(*sbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: read sing-box config: %v\n", err)
		os.Exit(1)
	}

	mgr := subscription.NewManager(subscription.NewHTTPFetcher())
	nodes, loadErrs := mgr.Load(context.Background(), cfg.Subscriptions)
	for _, e := range loadErrs {
		fmt.Fprintf(os.Stderr, "warn: %v\n", e)
	}

	groups := groupBySubscription(nodes)
	res, err := singbox.Merge(existing, groups, singbox.MergeOptions{
		SelectorTag: *selector, ProbeURL: *probeURL, Interval: *interval, UDPMode: *udpMode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: %v\n", err)
		os.Exit(1)
	}

	for _, s := range res.Skipped {
		fmt.Fprintf(os.Stderr, "skip: %v\n", s)
	}
	fmt.Fprintf(os.Stderr, "merged: %d nodes into %d pools (%s); selector %q updated\n",
		res.NodeCount, len(res.Pools), strings.Join(res.Pools, ", "), *selector)
	if len(res.UDPGroup) > 0 {
		fmt.Fprintf(os.Stderr, "udp (%s): vpn-udp -> [%s]\n", *udpMode, strings.Join(res.UDPGroup, ", "))
	}

	if *out == "" {
		os.Stdout.Write(res.JSON)
		fmt.Println()
		return
	}
	if err := os.WriteFile(*out, res.JSON, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "merge: write: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
}

// groupBySubscription buckets nodes by their source subscription, preserving
// first-seen order, and names each pool "<source>-pool".
func groupBySubscription(nodes []subscription.Node) []singbox.NodeGroup {
	order := []string{}
	bySrc := map[string][]subscription.Node{}
	for _, n := range nodes {
		if _, ok := bySrc[n.Source]; !ok {
			order = append(order, n.Source)
		}
		bySrc[n.Source] = append(bySrc[n.Source], n)
	}
	groups := make([]singbox.NodeGroup, 0, len(order))
	for _, src := range order {
		groups = append(groups, singbox.NodeGroup{
			PoolName: poolName(src),
			Nodes:    bySrc[src],
		})
	}
	return groups
}

func poolName(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	s = strings.ReplaceAll(s, " ", "-")
	if s == "" {
		s = "sub"
	}
	return s + "-pool"
}

// harvest runs blockcheck (SIMULATE by default — no data-plane touch) to dump the
// full strategy search space, then imports the distinct nfqws strategies as
// catalog definitions. This is how Lotsman acquires zapret's curated strategy
// list without re-deriving it: run on the router, capture, learn.
func harvest(args []string) {
	fs := flag.NewFlagSet("harvest", flag.ExitOnError)
	script := fs.String("script", "/opt/zapret/blockcheck.sh", "path to blockcheck.sh")
	base := fs.String("zapret-base", "/opt/zapret", "zapret install root (binaries at <base>/binaries/<platform>)")
	platform := fs.String("platform", "linux-arm64", "binary subdir under <base>/binaries")
	domains := fs.String("domains", "rutracker.org", "comma-separated target domains")
	scan := fs.String("scanlevel", "force", "quick|standard|force (force = widest search space)")
	simulate := fs.Bool("simulate", true, "SIMULATE=1: dry harvest, no network/nfqws (safe to run anytime)")
	outPath := fs.String("out", "", "merge discovered strategies into this JSON catalog file (LOT-10a; empty = stdout only). The daemon reads it via -strategy-catalog-file.")
	_ = fs.Parse(args)

	r := blockcheck.New(nil)
	out, err := r.RunRaw(context.Background(), blockcheck.Options{
		ScriptPath: *script,
		ZapretBase: *base,
		Platform:   *platform,
		Domains:    strings.Split(*domains, ","),
		ScanLevel:  *scan,
		Simulate:   *simulate,
	})
	if err != nil {
		// blockcheck may exit non-zero yet still produce a usable dump; warn, continue.
		fmt.Fprintf(os.Stderr, "harvest: %v\n", err)
	}

	defs := blockcheck.ToDefinitions(blockcheck.ParseEnumerated(string(out)))
	if len(defs) == 0 {
		fmt.Fprintln(os.Stderr, "harvest: no nfqws strategies parsed (check -script / output)")
		os.Exit(1)
	}
	for _, d := range defs {
		fmt.Printf("%-28s  %s\n", d.ID, strings.Join(d.NFQWSArgs, " "))
	}
	fmt.Fprintf(os.Stderr, "harvested %d distinct nfqws strategies (simulate=%v, scanlevel=%s)\n", len(defs), *simulate, *scan)

	// LOT-10a: persist the discovered strategies so the daemon can rank/render
	// them. Merge over any prior harvest (by ID) so the search space accumulates.
	if *outPath != "" {
		prior, err := strategy.LoadDefinitions(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "harvest: read existing catalog %s: %v\n", *outPath, err)
			os.Exit(1)
		}
		merged := strategy.MergeDefinitions(prior, defs)
		if err := strategy.SaveDefinitions(*outPath, merged); err != nil {
			fmt.Fprintf(os.Stderr, "harvest: write catalog %s: %v\n", *outPath, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "catalog %s: %d total (%d new this run)\n", *outPath, len(merged), len(merged)-len(prior))
	}
}

// status is the operator's at-a-glance view of the LIVE daemon. It scrapes the
// Prometheus /metrics text for per-service health gauges (position/state, broken,
// leak/dead-flow ratios, flows, misroute, fails) and, for each service seen,
// queries the clash-api selector (sel-<service>) to show the currently chosen
// node. Read-only: a down daemon yields a clear error and a non-zero exit.
func status(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	metricsAddr := fs.String("metrics-addr", "127.0.0.1:9101", "daemon Prometheus metrics host:port")
	clashBase := fs.String("clash-base", "http://127.0.0.1:9090", "clash-api base URL")
	clashSecret := fs.String("clash-secret", "", "clash-api secret (optional)")
	_ = fs.Parse(args)

	ctx := context.Background()

	text, err := fetchMetrics(ctx, *metricsAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: daemon metrics unreachable at %s: %v\n", *metricsAddr, err)
		fmt.Fprintln(os.Stderr, "       is lotsmand running? (check /etc/init.d/lotsman status)")
		os.Exit(1)
	}

	byService := parseMetrics(text)
	if len(byService) == 0 {
		fmt.Fprintln(os.Stderr, "status: daemon is up but reported no services (still warming up?)")
		os.Exit(1)
	}

	svcs := make([]string, 0, len(byService))
	for s := range byService {
		svcs = append(svcs, s)
	}
	sort.Strings(svcs)

	// Resolve the live selector node per service from the clash-api. Best-effort:
	// the metrics view is the source of truth for health; a clash hiccup just
	// shows "?" for the node rather than failing the whole command.
	clash := dataplane.NewClashClient(*clashBase, *clashSecret)
	nodes := map[string]string{}
	for _, svc := range svcs {
		info, err := clash.Proxy(ctx, registry.SelectorTag(svc))
		if err != nil {
			nodes[svc] = "?"
			continue
		}
		nodes[svc] = info.Now
	}

	printStatusTable(os.Stdout, svcs, byService, nodes)
}

// fetchMetrics GETs the daemon's /metrics text.
func fetchMetrics(ctx context.Context, addr string) (string, error) {
	url := "http://" + addr + "/metrics"
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// printStatusTable renders the per-service table and a footer summary. A leading
// "!" marks a service that is broken or misrouted — the rows an operator should
// look at first.
func printStatusTable(w io.Writer, svcs []string, byService map[string]*serviceStatus, nodes map[string]string) {
	fmt.Fprintln(w, "== Lotsman live status ==")
	fmt.Fprintf(w, "%-2s %-14s %-12s %-22s %6s %6s %-12s %5s\n",
		"", "SERVICE", "STATE(pos)", "NODE (sel→now)", "leak", "dead", "misrouted", "fails")

	misrouted := 0
	broken := 0
	for _, svc := range svcs {
		s := byService[svc]
		flag := " "
		if s.Broken || s.Misrouted {
			flag = "!"
		}
		if s.Broken {
			broken++
		}
		mis := "-"
		if s.Misrouted {
			misrouted++
			mis = "YES"
			if s.MisrouteKind != "" {
				mis = s.MisrouteKind
			}
		}
		state := s.State
		if state == "" {
			state = "?"
		}
		node := nodes[svc]
		if node == "" {
			node = "?"
		}
		bk := ""
		if s.Broken {
			bk = " BROKEN"
		}
		fmt.Fprintf(w, "%-2s %-14s %-12s %-22s %6.2f %6.2f %-12s %5d%s\n",
			flag, svc, fmt.Sprintf("%s(%d)", state, s.Position),
			node, s.LeakRatio, s.DeadFlowRatio, mis, s.Fails, bk)
	}

	fmt.Fprintf(w, "\n%d services · %d misrouted · %d broken\n", len(svcs), misrouted, broken)
	if misrouted == 0 && broken == 0 {
		fmt.Fprintln(w, "all services healthy ✓")
	}
}

func generate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	configPath := fs.String("config", "", "path to YAML config (required)")
	out := fs.String("out", "", "output path for sing-box config (default: stdout)")
	clashListen := fs.String("clash-listen", "127.0.0.1:9090", "clash-api external_controller")
	tproxyPort := fs.Int("tproxy-port", 7893, "tproxy inbound port")
	socksProbe := fs.String("socks-probe", "", "socks probe-in inbound host:port (empty = none)")
	_ = fs.Parse(args)

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "generate: -config is required")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: config: %v\n", err)
		os.Exit(1)
	}

	mgr := subscription.NewManager(subscription.NewHTTPFetcher())
	nodes, loadErrs := mgr.Load(context.Background(), cfg.Subscriptions)
	for _, e := range loadErrs {
		fmt.Fprintf(os.Stderr, "warn: %v\n", e)
	}

	memberships := cfg.Pools.Memberships(nodes)

	services := make([]registry.Service, 0, len(cfg.Registry.Services))
	for _, s := range cfg.Registry.Services {
		services = append(services, s)
	}

	opts := singbox.DefaultOptions()
	opts.ClashAPIListen = *clashListen
	opts.TproxyPort = *tproxyPort
	opts.SocksProbeListen = *socksProbe
	opts.PoolOpts = singbox.PoolOptionsFrom(cfg.Pools)
	opts.UTLSFingerprint = cfg.UTLSFingerprint
	if cfg.SingboxVersion != "" {
		opts.TargetVersion = cfg.SingboxVersion
	}
	if cfg.FakeIP != nil {
		opts.FakeIP = &singbox.FakeIPOptions{Inet4Range: cfg.FakeIP.Inet4Range, Inet6Range: cfg.FakeIP.Inet6Range, Resolver: cfg.FakeIP.Resolver}
	}
	if cfg.Multiplex != nil {
		opts.Multiplex = &singbox.MultiplexOptions{Protocol: cfg.Multiplex.Protocol, MaxConnections: cfg.Multiplex.MaxConnections, MinStreams: cfg.Multiplex.MinStreams, Padding: cfg.Multiplex.Padding, BrutalUp: cfg.Multiplex.BrutalUp, BrutalDown: cfg.Multiplex.BrutalDown}
	}

	res, err := singbox.Generate(services, cfg.Devices, nodes, memberships, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}

	for _, s := range res.Skipped {
		fmt.Fprintf(os.Stderr, "skip: %v\n", s)
	}
	fmt.Fprintf(os.Stderr, "generated: %d nodes loaded, %d skipped\n", len(nodes), len(res.Skipped))
	for name, members := range memberships {
		fmt.Fprintf(os.Stderr, "  pool %s: %d nodes\n", name, len(members))
	}

	if *out == "" {
		os.Stdout.Write(res.JSON)
		fmt.Println()
		return
	}
	if err := os.WriteFile(*out, res.JSON, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "generate: write: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
}
