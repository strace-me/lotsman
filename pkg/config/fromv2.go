package config

import (
	"fmt"
	"sort"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// Gaps are the things the runtime can express that a document did not SAY. They are
// reported rather than hidden because the failure they prevent is silent: the config
// loads, the daemon starts, the rules route — and the operator has no way to tell the
// tun has no split-DNS, or that TLS outbounds carry no uTLS fingerprint. Each of those
// is a working system quietly running with a default nobody chose.
//
// The list is COMPUTED from the document, not a constant. A fixed list is wrong the
// moment any of these becomes declarable, because it then claims a gap that does not
// exist — and a gap report that lies about which gaps are open is worse than none: it
// trains the reader to ignore the one place the system admits what it is not doing.
func GapsFor(cfg *configv2.Configuration) []string {
	if cfg == nil {
		return nil
	}
	var gaps []string
	// cfg.DNS is a pointer, and a document that declares no dns section leaves it nil.
	// Reading a field off that is not a "cosmetic" nil check — it is the difference
	// between reporting a gap and panicking while reporting it.
	if cfg.DNS == nil || len(cfg.DNS.Servers) == 0 {
		gaps = append(gaps, "dns: no dns section declared, so the tun resolves through the runtime's own default "+
			"(no split-DNS, no failover, no direct resolver of its own — a direct/zapret rung cannot name a target)")
	}
	if cfg.UTLSFingerprint == "" {
		gaps = append(gaps, "utls_fingerprint: none declared, so every TCP TLS outbound goes with no pinned "+
			"uTLS fingerprint. A default, not a data loss")
	}
	if len(cfg.Strategies) == 0 {
		gaps = append(gaps, "strategies: none declared, so only the builtin catalog and the on-disk bundle "+
			"store are in scope — a rule whose rung names a strategy that is in neither cannot resolve")
	}
	if cfg.Desync.Override != nil {
		gaps = append(gaps, "desync.override: set, so per-rule recipe learning is OFF for every rule — one "+
			"bundle serves all of them and there is nothing left to choose between")
	}
	if len(cfg.Desync.Instances) > 0 {
		for _, name := range desyncInstanceNames(cfg) {
			if cfg.Desync.Instances[name].Engine == "" {
				gaps = append(gaps, fmt.Sprintf("desync instance %q declares no engine: the adapter that launches "+
					"it is what picks nfqws1 or nfqws2, so this instance runs whichever the platform defaults to", name))
			}
		}
	}
	sort.Strings(gaps)
	return gaps
}

// desyncInstanceNames lists declared instances in a stable order, so a gap about one of
// them does not move around between runs.
func desyncInstanceNames(cfg *configv2.Configuration) []string {
	out := make([]string, 0, len(cfg.Desync.Instances))
	for name := range cfg.Desync.Instances {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FromV2 projects a v2 document onto the Config the runtime already consumes.
//
// This is the whole cutover. Everything downstream — Brain, Applier, the executors,
// the nft renderer, singbox.Generate — reads Config/registry.Service, so making the
// document a live parser is a matter of producing the same shape from it, not of a
// second runtime. It lives HERE rather than in pkg/configv2 because pkg/config already
// imports configv2 for the migration, and the reverse import would close a cycle.
func FromV2(cfg *configv2.Configuration, rules []configv2.Rule) (*Config, []string, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("no configuration")
	}
	reg, err := configv2.BuildRegistry(cfg, rules, nil, nil)
	if err != nil {
		return nil, nil, err
	}

	out := &Config{Registry: reg}
	// The detour graph is carried as pool -> pool. Cycles were already refused at
	// load, so what arrives here is a forest; the generator only ever walks it to
	// decide which nodes need a detoured variant.
	//
	// Initialised FIRST: allocating the map after filling it wiped every entry and the
	// generator quietly produced a config with no composition in it, which looks exactly
	// like "the feature does nothing".
	out.Detours = map[string]string{}
	// Lists the operator handed to sing-box whole, re-keyed to the rule-set TAG so the
	// generator can emit a `remote` definition instead of looking for a local .srs it
	// will never find.
	remote := map[string]string{}
	collect := func(kind string, defs map[string]configv2.List) {
		for name, def := range defs {
			if def.SRSURL == "" {
				continue
			}
			if tag, err := cfg.ResolveListTag(name, kind); err == nil {
				remote[tag] = def.SRSURL
			}
		}
	}
	collect("geosite", cfg.Lists.Domains)
	collect("geoip", cfg.Lists.IPs)
	if len(remote) > 0 {
		out.RemoteRuleSets = remote
	}
	for _, rule := range rules {
		for _, step := range rule.Chain {
			if vpn := step.Action.VPN; vpn != nil && vpn.Detour != "" {
				out.Detours[vpn.Pool] = vpn.Detour
			}
		}
	}
	// The target version must reach the generator, or it runs at its 1.12.17 baseline
	// and strips version-gated node features — which is not a missing knob, it is a node
	// quietly losing its obfs.
	out.SingboxVersion = cfg.TargetVersion
	out.UTLSFingerprint = cfg.UTLSFingerprint
	out.SubViaPool = cfg.SubViaPool

	out.Devices = make([]registry.Device, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		out.Devices = append(out.Devices, registry.Device{
			Name: d.Name, Sources: append([]string{}, d.Sources...), Policy: d.Policy,
		})
	}

	out.Subscriptions = make([]subscription.Declaration, 0, len(cfg.Subscriptions))
	for _, s := range cfg.Subscriptions {
		out.Subscriptions = append(out.Subscriptions, subscription.Declaration{
			Name: s.Name, URL: s.URL, Format: subscription.Format(s.Format),
			Tags: append([]string{}, s.Tags...), Enabled: s.Enabled, Expires: s.Expires,
		})
	}

	// Pools are seeded from the builtins and then OVERLAID by the document, so a partial
	// document still gets the standard latency and emergency pools. Without the overlay a
	// partial file would silently delete the emergency rung every rule falls back to —
	// and the symptom of a deleted emergency pool is a rule whose last resort is gone,
	// discovered during an outage rather than at load.
	set := pools.Builtin()
	injectBuiltinPools(set, reg, cfg.SubViaPool, out.Devices)
	for name, p := range cfg.Pools {
		pool := pools.Pool{
			Name:        name,
			Type:        p.Type,
			Limit:       p.Limit,
			MinCoverage: p.MinCoverage,
			Warmup:      p.Warmup,
			Filter: pools.Filter{
				Caps:             p.Caps,
				TagsInclude:      p.TagsInclude,
				TagsExclude:      p.TagsExclude,
				CountriesInclude: lowerAll(p.CountriesInclude),
				CountriesExclude: lowerAll(p.CountriesExclude),
			},
		}
		// UNSET IS NOT ZERO. An empty interval means "let the generator pick its default"
		// (5m, or 1m for a warmup pool); "0s" means "never stop probing". Writing the zero
		// through turned every unset interval into a pool that probes forever, which the
		// byte-comparison caught as a real difference against the previous output — the
		// same distinction `sticky` needs a pointer for.
		if p.Interval.Value() > 0 {
			pool.Interval = p.Interval.Value().String()
		}
		if p.IdleTimeout.Value() > 0 {
			pool.IdleTimeout = p.IdleTimeout.Value().String()
		}
		set.Pools[name] = pool
	}
	out.Pools = set

	// The nfqws instances the document declares are the managed unit, same as ever: a
	// process, a queue, a port set.
	names := desyncInstanceNames(cfg)
	// Position is the alphabetical order, deliberately: it decides the queue numbers, and
	// a queue that CHANGES because a map iterated differently would move an installed
	// instance's traffic to another queue without anyone asking.
	for i, name := range names {
		inst := cfg.Desync.Instances[name]
		// The queue number is DERIVED from the instance's position. Leaving it at 0 was
		// caught by the live cutover: the allocator refuses a production queue of 0, so the
		// daemon refused to start at all rather than running with an unusable queue.
		out.Zapret = append(out.Zapret, zapret.Instance{
			Name:      name,
			QNum:      DeriveQNum(i),
			Capture:   zapret.Capture{TCP: inst.Capture.TCP, UDP: inst.Capture.UDP},
			Connbytes: inst.Connbytes,
		})
	}
	if err := zapret.Validate(out.Zapret); err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}

	// The sections that carry their own resolution rules resolve here, from the same
	// builders the loader validated with, so a document cannot pass Validate and then
	// fail differently on the way to the runtime.
	hostlists, err := configv2.BuildHostlists(cfg.Hostlists)
	if err != nil {
		return nil, nil, err
	}
	out.Hostlists = hostlists
	if out.Strategies, err = configv2.BuildStrategies(cfg.Strategies); err != nil {
		return nil, nil, err
	}
	if out.DNS, err = configv2.BuildDNS(cfg.DNS); err != nil {
		return nil, nil, err
	}
	if cfg.Multiplex != nil && cfg.Multiplex.Enabled {
		out.Multiplex = &configv2.Multiplex{
			Protocol:       cfg.Multiplex.Protocol,
			MaxConnections: cfg.Multiplex.MaxConnections,
			MinStreams:     cfg.Multiplex.MinStreams,
			Padding:        cfg.Multiplex.Padding,
			BrutalUp:       cfg.Multiplex.BrutalUp,
			BrutalDown:     cfg.Multiplex.BrutalDown,
		}
	}
	if cfg.Desync.Override != nil {
		if out.DesyncOverride, err = configv2.BuildOverride(*cfg.Desync.Override); err != nil {
			return nil, nil, err
		}
	}

	// A disabled rule is dropped from the registry entirely, so nothing probes, routes or
	// desyncs it. The name is carried separately only so a UI can list it and offer to
	// switch it back on. Bypassed rules are carried SEPARATELY from disabled ones, though
	// both are absent from the runtime: `enabled: false` is a switch the operator turned
	// off, `bypass: true` is a standing statement that these domains are not Lotsman's
	// business, and a UI that offers to switch one back on must not offer it for the other.
	var bypassed []string
	for _, rule := range rules {
		if rule.Bypass {
			bypassed = append(bypassed, rule.ID)
			continue
		}
		if !rule.Enabled {
			out.DisabledServices = append(out.DisabledServices, rule.ID)
		}
	}
	sort.Strings(bypassed)
	out.BypassedServices = bypassed
	sort.Strings(out.DisabledServices)

	return out, GapsFor(cfg), nil
}
