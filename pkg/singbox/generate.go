package singbox

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/strace-me/lotsman/pkg/affinity"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// PoolOptions tunes one generated url-test group. Empty fields fall back to the
// generator defaults (interval 5m, no idle_timeout → sing-box sleeps the pool
// when unused). A warmup pool sets IdleTimeout "0s" so it never sleeps and its
// nodes stay continuously probed — failover is then instant, not gated on a
// cold re-probe.
type PoolOptions struct {
	Interval    string
	IdleTimeout string
}

// PoolOptionsFrom derives per-pool generator options from the pool set, applying
// the warmup defaults in one place (so reconcile and lotsmanctl agree): a warmup
// pool with no explicit interval probes every 1m and never idles. Returns nil
// for a nil set (all generator defaults).
func PoolOptionsFrom(set *pools.Set) map[string]PoolOptions {
	if set == nil {
		return nil
	}
	out := make(map[string]PoolOptions, len(set.Pools))
	for name, p := range set.Pools {
		o := PoolOptions{Interval: p.Interval, IdleTimeout: p.IdleTimeout}
		if p.Warmup {
			if o.Interval == "" {
				o.Interval = "1m"
			}
			if o.IdleTimeout == "" {
				o.IdleTimeout = "0s"
			}
		}
		out[name] = o
	}
	return out
}

// inbounds builds the inbound list: the tproxy that catches br-lan traffic, plus
// an optional socks "probe-in" so the box can probe through its own LAN path
// (matching the real R5S). A malformed SocksProbeListen is skipped, not fatal —
// probing just falls back to direct.
func inbounds(opts Options) []any {
	in := []any{map[string]any{
		"type": "tproxy", "tag": "tproxy-in",
		"listen": "0.0.0.0", "listen_port": opts.TproxyPort,
	}}
	if opts.SocksProbeListen != "" {
		if host, portStr, err := net.SplitHostPort(opts.SocksProbeListen); err == nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				in = append(in, map[string]any{
					"type": "socks", "tag": "probe-in",
					"listen": host, "listen_port": port,
				})
			}
		}
	}
	return in
}

// Options grounds generation in the target deployment. Defaults match the R5S.
type Options struct {
	TproxyPort       int                    // tproxy inbound port
	ClashAPIListen   string                 // experimental.clash_api external_controller
	DefaultMark      int                    // route.default_mark (sing-box's own traffic mark)
	RuleSetDir       string                 // base dir holding rule-set-{geosite,geoip}/*.srs
	SocksProbeListen string                 // socks "probe-in" inbound host:port (empty = none); lets the box probe via the LAN path
	PoolOpts         map[string]PoolOptions // per-pool url-test tuning (interval/idle_timeout); nil = defaults
	UTLSFingerprint  string                 // default tls.utls fingerprint for TCP TLS outbounds lacking one (e.g. "chrome"); "" = off
	TargetVersion    string                 // sing-box version to target (e.g. "1.12.17"); gates version-specific knobs. "" = baseline
	FakeIP           *FakeIPOptions         // emit a fakeip DNS section (nil = off)
	Multiplex        *MultiplexOptions      // default outbound multiplex for TCP proxies (nil = off)
	Remediations     map[string]Remediation // per-service self-heal remediation rules (nil/absent = no change; LOT-18). Keyed by service name.

	// RemHotReload (LOT-34) emits PERMANENT reject-QUIC + ip-fallback rules that
	// match LOCAL rule_sets (rem-rq-<svc> / rem-fb-<svc>) whose membership the armed
	// controller toggles by rewriting the rule_set files (RemRuleSetSource) — which
	// sing-box hot-reloads (since 1.10.0), so apply/revert never restarts sing-box
	// (a restart drops ALL connections). When on, these PERMANENT rules replace the
	// inline opts.Remediations path for RemServices. Off (default) => byte-identical
	// to the inline behaviour. The toggle files MUST exist before sing-box loads
	// this config (EnsureRemFiles); RemDir holds them ("" => RuleSetDir).
	RemHotReload bool
	RemServices  []string // services that get the permanent rem rule_set scaffold
	RemDir       string   // dir for the toggle files ("" => RuleSetDir)

	// SubViaHosts/SubViaPool route the subscription-endpoint hosts through a VPN
	// pool instead of final:direct (LOT-28), so a subscription refresh egresses
	// abroad — the flaky `acme` mirror is unreliable to reach from the RU network
	// directly. A domain_suffix rule for these hosts -> SubViaPool is emitted at the
	// top of the domain tier. Opt-in and fail-safe: if SubViaPool is empty, the
	// pool is empty, or no hosts are given, NO rule is emitted (byte-identical to
	// before) and the fetch falls back to direct. Pair with a proxy fetcher
	// (NewHTTPFetcherProxy) so the fetch actually enters sing-box to be routed.
	SubViaHosts []string // subscription endpoint hostnames to route via the tunnel
	SubViaPool  string   // pool tag to route SubViaHosts through ("" = off)
}

// Remediation describes the self-heal rules to inject for one service (LOT-18).
// It is opt-in and additive: a service with no Remediation entry generates
// EXACTLY as before (byte-identical output), so PROPOSE-ONLY mode (18a, nothing
// passed) cannot alter the live config. Two independent primitives:
//
//   - RejectQUIC: emit a route rule matching this service on network:udp,
//     port:443 with action:reject, placed BEFORE the service's normal rule so it
//     wins for UDP/443 — forcing the client to fall back to TLS-over-TCP (which
//     sniffs reliably). Matches the service the same way its normal rule does
//     (rule_set/domain_suffix/ip_cidr).
//   - FallbackCIDRs: emit an ip_cidr rule for these CIDRs -> the service's
//     sel-<svc> selector, so un-sniffed QUIC reaches the tunnel by IP instead of
//     leaking to direct. Ignored for a direct-only service (it has no selector).
type Remediation struct {
	RejectQUIC    bool     // reject this service's udp/443 (QUIC-kill, TCP fallback)
	FallbackCIDRs []string // CIDRs routed to sel-<svc> (IP-fallback for un-sniffed QUIC)
}

// MultiplexOptions configures the default `multiplex` block injected into TCP
// proxy outbounds (vless/vmess/trojan/shadowsocks) that don't already declare
// one. Multiplexing carries many streams over fewer connections, which breaks
// per-flow correlation; padding pads the stream framing, and brutal pins a
// congestion target. Server-cooperative (the server must enable mux too), so
// it's off by default. QUIC outbounds (hysteria2/tuic) and anytls (itself a
// muxing protocol) are never touched.
type MultiplexOptions struct {
	Protocol       string // smux (default) | yamux | h2mux
	MaxConnections int    // 0 = omit (sing-box default)
	MinStreams     int    // 0 = omit
	Padding        bool   // pad the mux framing (anti fingerprint)
	BrutalUp       int    // brutal up_mbps (0 = no brutal)
	BrutalDown     int    // brutal down_mbps (0 = no brutal)
}

// FakeIPOptions configures the generated fakeip DNS section. With it, the box
// hands a synthetic IP per domain so routing keys off the DOMAIN (recovered from
// the original query) instead of a real lookup — making domain rules work even
// for ECH/QUIC/non-TLS, and dissolving the "same real IP, different domains"
// ambiguity. Verified form on sing-box 1.12.17: server type:"fakeip" + a real
// resolver + a DNS rule sending A/AAAA to fakeip; route sniff must be on.
type FakeIPOptions struct {
	Inet4Range string // default 198.18.0.0/15
	Inet6Range string // default fc00::/18
	Resolver   string // real upstream for non-fake queries: "https://1.1.1.1/dns-query" or "192.168.1.1:5353" (udp)
}

// DefaultOptions returns the R5S-grounded defaults (see home-network-context).
func DefaultOptions() Options {
	return Options{
		TproxyPort:     7893,
		ClashAPIListen: "127.0.0.1:9090",
		DefaultMark:    255,
		RuleSetDir:     "/etc/sing-box",
		TargetVersion:  baselineVersion,
	}
}

// RuleSetRelPath maps a rule-set tag to its .srs path relative to the rule-set
// root (e.g. "geosite-youtube" -> "rule-set-geosite/geosite-youtube.srs"), and
// ok=false for an unknown prefix. Shared with pkg/rulesets so the updater swaps
// files at exactly the paths the generator references.
func RuleSetRelPath(tag string) (string, bool) {
	switch {
	case strings.HasPrefix(tag, "geosite-"):
		return "rule-set-geosite/" + tag + ".srs", true
	case strings.HasPrefix(tag, "geoip-"):
		return "rule-set-geoip/" + tag + ".srs", true
	}
	return "", false
}

// ruleSetDefs builds local rule-set definitions for the configured tags so the
// generated config is self-contained and passes `sing-box check` (given the
// .srs files exist, which sb-update-rules maintains). The prefix only selects
// the subdir; the filename keeps the FULL tag, matching the real R5S layout
// (e.g. "geosite-discord" -> <dir>/rule-set-geosite/geosite-discord.srs).
func ruleSetDefs(tags []string, dir string) []any {
	defs := make([]any, 0, len(tags))
	for _, tag := range tags {
		var sub string
		switch {
		case strings.HasPrefix(tag, "geosite-"):
			sub = "rule-set-geosite"
		case strings.HasPrefix(tag, "geoip-"):
			sub = "rule-set-geoip"
		default:
			continue // unknown prefix: skip rather than emit a broken path
		}
		defs = append(defs, map[string]any{
			"type":   "local",
			"tag":    tag,
			"format": "binary",
			"path":   dir + "/" + sub + "/" + tag + ".srs",
		})
	}
	return defs
}

// dnsBlock builds the fakeip DNS section in the form verified on sing-box 1.12.17
// (server type:"fakeip" + a real resolver + an A/AAAA->fakeip rule). Ranges default
// to the standard reserved blocks.
func dnsBlock(f *FakeIPOptions) map[string]any {
	in4, in6 := f.Inet4Range, f.Inet6Range
	if in4 == "" {
		in4 = "198.18.0.0/15"
	}
	if in6 == "" {
		in6 = "fc00::/18"
	}
	return map[string]any{
		"servers": []any{
			map[string]any{"tag": "fakeip", "type": "fakeip", "inet4_range": in4, "inet6_range": in6},
			dnsResolver(f.Resolver),
		},
		"rules":             []any{map[string]any{"query_type": []string{"A", "AAAA"}, "server": "fakeip"}},
		"final":             "resolver",
		"independent_cache": true,
	}
}

// dnsResolver renders the real upstream DNS server (non-fake queries). An
// https:// URL becomes a DoH server; otherwise host[:port] becomes a udp server.
func dnsResolver(resolver string) map[string]any {
	if resolver == "" {
		resolver = "https://1.1.1.1/dns-query"
	}
	if strings.HasPrefix(resolver, "https://") {
		// Parse properly so a non-standard DoH port and an IPv6 literal survive —
		// the old "cut at first :/" dropped the port and split the v6 address (LOT-38).
		host := resolver[len("https://"):]
		if u, err := url.Parse(resolver); err == nil && u.Host != "" {
			host = u.Host // keeps host[:port] and [v6][:port]; drops the /dns-query path
		}
		srv := map[string]any{"tag": "resolver", "type": "https"}
		if h, p, err := net.SplitHostPort(host); err == nil {
			host = h
			if port, err := strconv.Atoi(p); err == nil {
				srv["server_port"] = port
			}
		}
		srv["server"] = strings.Trim(host, "[]") // unbracket a port-less v6 literal
		return srv
	}
	host := resolver
	srv := map[string]any{"tag": "resolver", "type": "udp"}
	if h, p, err := net.SplitHostPort(resolver); err == nil {
		host = h
		if port, err := strconv.Atoi(p); err == nil {
			srv["server_port"] = port
		}
	}
	srv["server"] = host
	return srv
}

// svcRule attaches the route action to a service's match. Without fragmentation
// it uses the legacy `outbound` shorthand. With it, the explicit `action:route`
// form carries tls_fragment + tls_record_fragment so the TLS ClientHello on this
// service's connections is split across packets — a sing-box-native SNI-hiding
// knob (no nfqws needed). The match map is mutated in place and returned.
func svcRule(match map[string]any, target string, fragment bool) map[string]any {
	if fragment {
		match["action"] = "route"
		match["outbound"] = target
		match["tls_fragment"] = true
		match["tls_record_fragment"] = true
	} else {
		match["outbound"] = target
	}
	return match
}

// RemRuleSetSource renders a sing-box LOCAL rule_set in source format (version 2)
// holding the given domain suffixes + ip_cidrs. It is the TOGGLE FILE the armed
// remediation controller writes to arm/disarm a reject-QUIC or ip-fallback rule
// WITHOUT restarting sing-box: a permanent route rule matches this rule_set, and
// sing-box hot-reloads the local rule_set file on change (since 1.10.0) — so apply/
// revert is a file write, not a config rebuild + restart that drops all connections
// (LOT-34). Empty inputs render an empty rule set (matches nothing = disarmed/inert).
func RemRuleSetSource(domainSuffixes, ipCIDRs []string) []byte {
	type srcRule struct {
		DomainSuffix []string `json:"domain_suffix,omitempty"`
		IPCIDR       []string `json:"ip_cidr,omitempty"`
	}
	doc := struct {
		Version int       `json:"version"`
		Rules   []srcRule `json:"rules"`
	}{Version: 2, Rules: []srcRule{}}
	r := srcRule{DomainSuffix: sortedUniqueLower(domainSuffixes), IPCIDR: sortedUnique(ipCIDRs)}
	if len(r.DomainSuffix) > 0 || len(r.IPCIDR) > 0 {
		doc.Rules = append(doc.Rules, r)
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n')
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func sortedUniqueLower(in []string) []string {
	low := make([]string, len(in))
	for i, s := range in {
		low[i] = strings.ToLower(s)
	}
	return sortedUnique(low)
}

// RemRejectTag / RemFallbackTag are the local rule_set tags for a service's
// hot-reloadable reject-QUIC / ip-fallback remediation (LOT-34).
func RemRejectTag(svc string) string   { return "rem-rq-" + svc }
func RemFallbackTag(svc string) string { return "rem-fb-" + svc }

// RemFilePath is the toggle file for a rem rule_set tag under dir.
func RemFilePath(dir, tag string) string { return dir + "/" + tag + ".json" }

// remLocalDefs returns the local rule_set definitions for the hot-reload scaffold
// (LOT-34): a source-format local rule_set per eligible service for reject-QUIC and
// ip-fallback, each pointing at its toggle file. Empty when RemHotReload is off.
func remLocalDefs(opts Options) []any {
	if !opts.RemHotReload {
		return nil
	}
	dir := opts.RemDir
	if dir == "" {
		dir = opts.RuleSetDir
	}
	svcs := append([]string(nil), opts.RemServices...)
	sort.Strings(svcs)
	var out []any
	for _, s := range svcs {
		for _, tag := range []string{RemRejectTag(s), RemFallbackTag(s)} {
			out = append(out, map[string]any{
				"type": "local", "tag": tag, "format": "source", "path": RemFilePath(dir, tag),
			})
		}
	}
	return out
}

// EnsureRemFiles writes an empty (disarmed) toggle file for each service's rem
// rule_sets if absent — the precondition for sing-box to load a config referencing
// these local rule_sets (LOT-34). Existing files are preserved (keeps an armed
// state across restarts). Idempotent.
func EnsureRemFiles(dir string, services []string) error {
	empty := RemRuleSetSource(nil, nil)
	for _, s := range services {
		for _, tag := range []string{RemRejectTag(s), RemFallbackTag(s)} {
			p := RemFilePath(dir, tag)
			if _, err := os.Stat(p); err == nil {
				continue
			}
			if err := os.WriteFile(p, empty, 0o644); err != nil {
				return fmt.Errorf("ensure rem file %s: %w", p, err)
			}
		}
	}
	return nil
}

// rejectQUICRules builds the reject-QUIC route rules for a service (LOT-18): one
// rule per match kind (rule_set / domain_suffix / ip_cidr), each constrained to
// network:["udp"], port:[443] with action:"reject". One rule per match kind
// preserves OR semantics across kinds (a single rule's fields AND together, so
// rule_set+ip_cidr in one rule would wrongly require both). Emitted into a tier
// ahead of the service's normal rules so QUIC on udp/443 is rejected — forcing
// the client to retry over TCP, which sniffs reliably — while TCP/443 still
// follows the normal route.
func rejectQUICRules(svc registry.Service) []any {
	var out []any
	add := func(matchKey string, matchVal any) {
		out = append(out, map[string]any{
			matchKey:  matchVal,
			"network": []string{"udp"},
			"port":    []int{443},
			"action":  "reject",
		})
	}
	if len(svc.RuleSets) > 0 {
		add("rule_set", svc.RuleSets)
	}
	if len(svc.Domains) > 0 {
		add("domain_suffix", svc.Domains)
	}
	if len(svc.IPs) > 0 {
		add("ip_cidr", svc.IPs)
	}
	return out
}

// dedupNonEmpty returns the input with blanks dropped and duplicates removed,
// preserving first-seen order (stable generated output).
func dedupNonEmpty(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Result is a generated config plus the nodes that were skipped (unsupported
// protocol), so the caller can report coverage.
type Result struct {
	JSON         []byte
	Skipped      []SkipError // nodes dropped (unsupported protocol)
	SkippedKnobs []string    // knobs requested but not emitted because the target sing-box version lacks them
}

// Generate builds a sing-box config from services, nodes and pool memberships.
//
//   - Every supported node becomes an outbound.
//   - Each pool becomes a url-test group over its members' outbound tags.
//   - Each service gets its OWN selector (registry.SelectorTag) so Lotsman can
//     route services independently; the service's rule-sets route to it, and it
//     defaults to the service's VPN pool (or direct if that pool is empty).
//   - Route preserves the R5S shape: private->direct, per-service rule-sets->
//     that service's selector, final direct, default_mark.
func Generate(services []registry.Service, devices []registry.Device, nodes []subscription.Node, memberships map[string][]subscription.Node, opts Options) (Result, error) {
	var res Result
	caps := Capabilities(opts.TargetVersion)

	// uTLS is version-gated: drop the default fingerprint (and report it) if the
	// target sing-box does not support utls, rather than emit an invalid field.
	utlsFP := opts.UTLSFingerprint
	if utlsFP != "" && !caps.Supports(FeatureUTLS) {
		res.SkippedKnobs = append(res.SkippedKnobs,
			fmt.Sprintf("utls fingerprint %q (unsupported on sing-box %s)", utlsFP, caps.Version()))
		utlsFP = ""
	}

	// multiplex is version-gated the same way: drop the default mux block (and
	// report it) rather than emit a field the target sing-box rejects.
	mux := opts.Multiplex
	if mux != nil && !caps.Supports(FeatureMultiplex) {
		res.SkippedKnobs = append(res.SkippedKnobs,
			fmt.Sprintf("multiplex (unsupported on sing-box %s)", caps.Version()))
		mux = nil
	}

	// ECH is a per-node tls block (from the node's own share-link); a target that
	// lacks it has the ech block stripped per node, reported once below.
	echOK := caps.Supports(FeatureECH)
	var echStripped bool

	// Node outbounds, plus a tag lookup so groups reference real outbounds only.
	// WireGuard is special: since sing-box 1.11 it is an `endpoints` entry (the
	// outbound form is deprecated), but its tag is still referenced like an
	// outbound by pools/selectors/route.
	outbounds := make([]outbound, 0, len(nodes)+len(services)+8)
	var endpoints []outbound
	tagOf := make(map[string]string, len(nodes)) // node ID -> outbound/endpoint tag
	for _, n := range nodes {
		if n.Protocol == subscription.ProtoWireGuard {
			if !caps.Supports(FeatureWireGuard) {
				res.SkippedKnobs = append(res.SkippedKnobs,
					fmt.Sprintf("wireguard node %q (endpoints form needs sing-box %s+)", n.ID, minVersion[FeatureWireGuard]))
				continue
			}
			ep, err := wireguardEndpoint(n, outboundTag(n))
			if err != nil {
				if se, ok := err.(SkipError); ok {
					res.Skipped = append(res.Skipped, se)
					continue
				}
				return res, err
			}
			endpoints = append(endpoints, ep)
			tagOf[n.ID] = ep["tag"].(string)
			continue
		}
		ob, extras, err := nodeOutbound(n)
		if err != nil {
			if se, ok := err.(SkipError); ok {
				res.Skipped = append(res.Skipped, se)
				continue
			}
			return res, err
		}
		// Protocol-level version gate: a node whose protocol the target sing-box
		// can't parse is dropped (reported) rather than emitted as an invalid type.
		if n.Protocol == subscription.ProtoShadowTLS && !caps.Supports(FeatureShadowTLS) {
			res.SkippedKnobs = append(res.SkippedKnobs,
				fmt.Sprintf("shadowtls node %q (unsupported on sing-box %s)", n.ID, caps.Version()))
			continue
		}
		// Knob injection applies to the primary and any siblings (each self-guards
		// by outbound type), so e.g. the shadowtls camouflage TLS gets the uTLS
		// fingerprint too while the chained ss is left untouched by multiplex.
		for _, o := range append([]outbound{ob}, extras...) {
			injectUTLS(o, utlsFP)
			injectMultiplex(o, mux)
			// A node that declared ech keeps it only if the target supports it;
			// otherwise strip it (reported once) rather than emit a rejected field.
			if !echOK && stripECH(o) {
				echStripped = true
			}
		}
		outbounds = append(outbounds, ob)
		outbounds = append(outbounds, extras...)
		tagOf[n.ID] = ob["tag"].(string)
	}
	if echStripped {
		res.SkippedKnobs = append(res.SkippedKnobs,
			fmt.Sprintf("ech (unsupported on sing-box %s)", caps.Version()))
	}

	// url-test group per pool (deterministic order).
	poolNames := make([]string, 0, len(memberships))
	for name := range memberships {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)

	nonEmptyPool := map[string]bool{}
	var poolTags []string
	for _, name := range poolNames {
		members := memberships[name]
		tags := make([]string, 0, len(members))
		for _, m := range members {
			if t, ok := tagOf[m.ID]; ok {
				tags = append(tags, t)
			}
		}
		if len(tags) == 0 {
			continue // empty pool: nothing to url-test
		}
		grp := outbound{
			"type":      "urltest",
			"tag":       name,
			"outbounds": tags,
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  "5m",
		}
		if po, ok := opts.PoolOpts[name]; ok {
			if po.Interval != "" {
				grp["interval"] = po.Interval
			}
			if po.IdleTimeout != "" {
				grp["idle_timeout"] = po.IdleTimeout // "0s" = warmup: never sleep
			}
		}
		outbounds = append(outbounds, grp)
		poolTags = append(poolTags, name)
		nonEmptyPool[name] = true
	}

	// Per-service selector + route rule. Order matters: sing-box route is
	// first-match-wins, so emit by Priority (lower first), then name for a stable
	// tie-break. Protective/specific rules (ru-direct, Priority<0) land before
	// broad catch-alls (ru-blocked web-blocked, Priority>0), so a domain in both
	// a RU-direct set and a blocked set goes direct, and a specific service
	// (youtube) wins over the catch-all.
	sort.Slice(services, func(i, j int) bool {
		if services[i].Priority != services[j].Priority {
			return services[i].Priority < services[j].Priority
		}
		return services[i].Name < services[j].Name
	})
	selectorOutbounds := append(append([]string{}, poolTags...), "direct")

	// sniff first (route action, the 1.11+ replacement for deprecated inbound
	// sniff) so domain rule-sets can match the sniffed host; then private->direct.
	var routeRules []any
	routeRules = append(routeRules,
		map[string]any{"action": "sniff"},
		map[string]any{"ip_is_private": true, "outbound": "direct"},
	)

	// Per-device source overrides come before destination rules so a device's
	// policy wins regardless of what it is connecting to.
	for _, d := range devices {
		if len(d.Sources) == 0 {
			continue
		}
		rule := map[string]any{"source_ip_cidr": d.Sources}
		switch {
		case d.Policy == "direct":
			rule["outbound"] = "direct"
		case d.Policy == "block":
			rule["action"] = "reject" // per-device kill/parental control
		case nonEmptyPool[d.Policy]:
			rule["outbound"] = d.Policy // a VPN pool
		default:
			rule["outbound"] = "direct" // unknown/empty pool -> fail safe
		}
		routeRules = append(routeRules, rule)
	}

	// Rules are tiered so DOMAIN matches globally beat IP matches: every service's
	// rule_set/domain_suffix rule is emitted before any service's ip_cidr rule.
	// Because the route sniffs SNI/Host, a TLS connection to an IP shared by two
	// services is disambiguated by the sniffed domain (tier 1) before any broad
	// ip_cidr (tier 2) can hijack it; ip_cidr then only catches SNI-less traffic
	// (voice UDP, ECH, dedicated blocks). Within each tier, service Priority order
	// (set above) is preserved.
	allRuleSets := map[string]bool{}
	remEligible := make(map[string]bool, len(opts.RemServices))
	if opts.RemHotReload {
		for _, s := range opts.RemServices {
			remEligible[s] = true
		}
	}
	var rejectRules, spreadRules, domainRules, ipRules []any
	for _, svc := range services {
		if len(svc.RuleSets) == 0 && len(svc.Domains) == 0 && len(svc.IPs) == 0 {
			continue // nothing to route for this service yet
		}
		// A hard-direct service (ru_direct / LOCKED) routes straight to the
		// "direct" outbound — never a flippable selector — so RU traffic can never
		// leak to a VPN exit ("по любому в direct"). Every other service gets its
		// own selector defaulting to its VPN pool.
		target := "direct"
		if !svc.DirectOnly() {
			target = registry.SelectorTag(svc.Name)
			def := svc.VPNPool()
			if def == "" || !nonEmptyPool[def] {
				def = "direct" // no/empty VPN pool -> fail safe to direct
			}
			// Selector members = the pools + direct, PLUS the concrete nodes of this
			// service's VPN pool. The concrete nodes make pkg/noderank's per-service
			// pin a valid PUT target (Clash 400s on a non-member); the pool stays the
			// `default`, so with noderank off the selector behaves as a dumb url-test
			// switcher. Pinning is opt-in and never changes the default.
			svcSelOut := append([]string{}, selectorOutbounds...)
			if pool := svc.VPNPool(); pool != "" {
				for _, m := range memberships[pool] {
					if t, ok := tagOf[m.ID]; ok {
						svcSelOut = append(svcSelOut, t)
					}
				}
			}
			outbounds = append(outbounds, outbound{
				"type":      "selector",
				"tag":       target,
				"outbounds": svcSelOut,
				"default":   def,
			})
		}
		// tls_fragment is version-gated: honor it only if the target sing-box
		// supports it, else drop it for this service and report (not silent).
		frag := svc.TLSFragment
		if frag && !caps.Supports(FeatureTLSFragment) {
			res.SkippedKnobs = append(res.SkippedKnobs,
				fmt.Sprintf("tls_fragment@%s (unsupported on sing-box %s)", svc.Name, caps.Version()))
			frag = false
		}
		// One rule per match kind, all -> this service's target (OR semantics; a
		// single rule with multiple fields would AND them). The whole service goes
		// to one outbound — its traffic is never split across paths. rule_set and
		// domain_suffix go in the domain tier; ip_cidr in the (later) IP tier.
		if len(svc.RuleSets) > 0 {
			domainRules = append(domainRules, svcRule(map[string]any{"rule_set": svc.RuleSets}, target, frag))
			for _, rs := range svc.RuleSets {
				allRuleSets[rs] = true
			}
		}
		if len(svc.Domains) > 0 {
			domainRules = append(domainRules, svcRule(map[string]any{"domain_suffix": svc.Domains}, target, frag))
		}
		if len(svc.IPs) > 0 {
			ipRules = append(ipRules, svcRule(map[string]any{"ip_cidr": svc.IPs}, target, frag))
		}
		// Per-client spread (LOT-23): pin each declared client CIDR to one concrete
		// pool node (rendezvous hash), so different clients of this service ride
		// different nodes. Each client gets source_ip_cidr+match -> node rules,
		// emitted in their own tier BEFORE the plain service rules so they win.
		if !svc.DirectOnly() && len(svc.SpreadClients) > 0 {
			var poolTags []string
			if pool := svc.VPNPool(); pool != "" {
				for _, m := range memberships[pool] {
					if t, ok := tagOf[m.ID]; ok {
						poolTags = append(poolTags, t)
					}
				}
			}
			sort.Strings(poolTags) // deterministic input to Spread
			for _, client := range svc.SpreadClients {
				if len(poolTags) == 0 {
					break // no nodes to spread across; leave the client on the selector
				}
				node := affinity.Spread(client+"|"+svc.Name, poolTags)
				if len(svc.RuleSets) > 0 {
					spreadRules = append(spreadRules, map[string]any{"source_ip_cidr": []string{client}, "rule_set": svc.RuleSets, "outbound": node})
				}
				if len(svc.Domains) > 0 {
					spreadRules = append(spreadRules, map[string]any{"source_ip_cidr": []string{client}, "domain_suffix": svc.Domains, "outbound": node})
				}
				if len(svc.IPs) > 0 {
					spreadRules = append(spreadRules, map[string]any{"source_ip_cidr": []string{client}, "ip_cidr": svc.IPs, "outbound": node})
				}
			}
		}

		// Self-heal remediation (LOT-18), opt-in per service. Absent => no rules
		// added => byte-identical to the no-remediation config. reject-QUIC rules
		// are collected in their own (earlier) tier so they win over this service's
		// normal domain/ip rules for udp/443; the IP-fallback rule joins the IP tier.
		if opts.RemHotReload && remEligible[svc.Name] {
			// LOT-34: PERMANENT rules matching toggleable LOCAL rule_sets. The armed
			// controller arms/reverts by rewriting the rule_set FILES (hot-reload),
			// never restarting sing-box. Reject-QUIC and ip-fallback live here instead
			// of the inline (restart-on-change) path below.
			rejectRules = append(rejectRules, map[string]any{
				"rule_set": []string{RemRejectTag(svc.Name)},
				"network":  []string{"udp"}, "port": []int{443}, "action": "reject",
			})
			if !svc.DirectOnly() {
				ipRules = append(ipRules, svcRule(map[string]any{"rule_set": []string{RemFallbackTag(svc.Name)}}, target, frag))
			}
		} else if rem, ok := opts.Remediations[svc.Name]; ok {
			if rem.RejectQUIC {
				rejectRules = append(rejectRules, rejectQUICRules(svc)...)
			}
			if len(rem.FallbackCIDRs) > 0 && !svc.DirectOnly() {
				ipRules = append(ipRules, svcRule(map[string]any{"ip_cidr": rem.FallbackCIDRs}, target, frag))
			}
		}
	}
	// Subscription-via-VPN (LOT-28): route the subscription endpoint hosts through
	// a VPN pool so a refresh egresses abroad instead of via the unreliable RU
	// direct path. Emitted at the top of the domain tier so it wins over any broad
	// catch-all (e.g. geosite-ru-blocked) that might also cover the host. Fail-safe:
	// only when a pool is named, that pool is non-empty, and hosts are present —
	// otherwise nothing is added and the fetch falls back to direct (final:direct).
	var subRules []any
	if opts.SubViaPool != "" {
		hosts := dedupNonEmpty(opts.SubViaHosts)
		switch {
		case !nonEmptyPool[opts.SubViaPool]:
			// Configured but the pool is empty -> no rule -> the fetch falls back to
			// direct. Surface it (don't silently route the subscription direct while
			// the operator believes it goes via VPN).
			res.SkippedKnobs = append(res.SkippedKnobs,
				fmt.Sprintf("subscription_via_pool %q (pool empty; subscription fetch falls back to direct)", opts.SubViaPool))
		case len(hosts) == 0:
			// No fetched hosts (all inline) — nothing to route, not an error.
		default:
			subRules = append(subRules, map[string]any{
				"domain_suffix": hosts,
				"outbound":      opts.SubViaPool,
			})
		}
	}

	// reject-QUIC tier first (so udp/443 is killed before the service's own match
	// could route it), then domain tier, then IP tier (see tiering note above).
	routeRules = append(routeRules, rejectRules...)
	routeRules = append(routeRules, subRules...)
	// Per-client spread (LOT-23): before the plain domain/IP service rules so a
	// client's source_ip_cidr+match wins over the service's shared selector.
	routeRules = append(routeRules, spreadRules...)
	routeRules = append(routeRules, domainRules...)
	routeRules = append(routeRules, ipRules...)

	// direct only: the legacy "block" special outbound is deprecated in
	// sing-box 1.11+ (removed in 1.13) in favor of the route "reject" action;
	// we do not reference it, so it is omitted.
	outbounds = append(outbounds, outbound{"type": "direct", "tag": "direct"})

	ruleSetTags := make([]string, 0, len(allRuleSets))
	for rs := range allRuleSets {
		ruleSetTags = append(ruleSetTags, rs)
	}
	sort.Strings(ruleSetTags)

	cfg := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"experimental": map[string]any{
			"clash_api": map[string]any{"external_controller": opts.ClashAPIListen},
		},
		// Inbound-level sniff/domain_strategy are deprecated in 1.11+; sniffing
		// is done by the route "sniff" action above.
		"inbounds":  inbounds(opts),
		"outbounds": outbounds,
		"route": map[string]any{
			"rule_set":              append(ruleSetDefs(ruleSetTags, opts.RuleSetDir), remLocalDefs(opts)...),
			"rules":                 routeRules,
			"final":                 "direct",
			"auto_detect_interface": true,
			"default_mark":          opts.DefaultMark,
		},
	}

	// WireGuard endpoints (1.11+ form). Tags here are referenced like outbounds.
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}

	// FakeIP DNS section (opt-in, version-gated). Off => no dns block at all (the
	// box's existing resolver/AdGuard stays in charge).
	if opts.FakeIP != nil {
		if caps.Supports(FeatureFakeIP) {
			cfg["dns"] = dnsBlock(opts.FakeIP)
		} else {
			res.SkippedKnobs = append(res.SkippedKnobs,
				fmt.Sprintf("fakeip (unsupported on sing-box %s)", caps.Version()))
		}
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return res, err
	}
	res.JSON = out
	return res, nil
}
