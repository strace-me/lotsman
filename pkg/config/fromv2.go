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

// Gaps are the things a v2 document cannot say yet, listed by what stays ZERO in
// the Config this produces.
//
// It is returned rather than hidden because the failure mode it prevents is
// silent: a v2 file loads, the daemon starts, the rules route — and the operator
// has no way to tell that the tun has no split-DNS, that TLS outbounds carry no
// uTLS fingerprint, and that no declared strategy is in scope. Each of those is a
// working system quietly running with a default nobody chose. "Скажи, что
// измерение не коснулось" — the gap has to be said by the code that knows it.
var Gaps = []string{
	"dns: v2 carries no split-DNS; the client's tun uses sing-box defaults (DESIGN §15 decided this is a deployment default, and it is NOT implemented as one yet)",
	"fakeip: v2 has no legacy fakeip block; superseded by dns on the v1 side",
	"multiplex: v2 has none; v1 defaults it off, so the practical gap is nil until someone turns it on",
	"utls_fingerprint: v2 declares none, so every TCP TLS outbound goes with no pinned uTLS fingerprint. A default, not a data loss",
	"strategies[]: v2 reads strategies from a BUNDLE store, not from a config list, so Config.Strategies stays empty and only the builtin catalog is in scope",
	"hostlists[]: subsumed by v2 lists, so no Hostlist entries are produced",
	"zapret.preset: read-only legacy adapter by decision; Config.ZapretPresets is empty",
	"subscription_via_pool: no longer a config knob; the fetch path is direct unless a pool is present",
}

// FromV2 projects a v2 document onto the Config the runtime already consumes.
//
// This is the whole cutover. Everything downstream — Brain, Applier, the
// executors, the nft renderer, singbox.Generate — reads Config/registry.Service,
// so making v2 a live parser is a matter of producing the same shape from a
// different document, not of a second runtime. It lives HERE rather than in
// pkg/configv2 because pkg/config already imports configv2 for MigrateToV2, and
// the reverse import would close a cycle.
func FromV2(cfg *configv2.Configuration, rules []configv2.Rule) (*Config, []string, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("no v2 configuration")
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
	// Initialised FIRST: allocating the map after filling it wiped every entry and
	// the generator quietly produced a config with no composition in it, which looks
	// exactly like "the feature does nothing".
	out.Detours = map[string]string{}
	// Lists the operator handed to sing-box whole, re-keyed to the rule-set TAG so
	// the generator can emit a `remote` definition instead of looking for a local
	// .srs it will never find.
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
	// The target version must reach the generator, or it runs at its 1.12.17
	// baseline and strips version-gated node features — which is not a missing
	// knob, it is a node quietly losing its obfs.
	out.SingboxVersion = cfg.TargetVersion
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

	// Pools are seeded from the builtins and then OVERLAID by the document, so a
	// v2 file that declares only `bulk` still gets the standard latency and
	// emergency pools. Without the overlay a partial file would silently delete
	// the emergency rung every rule falls back to — and the symptom of a deleted
	// emergency pool is a rule whose last resort is gone, discovered during an
	// outage rather than at load.
	set := pools.Builtin()
	injectBuiltinPools(set, reg, "", out.Devices)
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
		// UNSET IS NOT ZERO. An empty interval means "let the generator pick its
		// default" (5m, or 1m for a warmup pool); "0s" means "never stop
		// probing". Writing the zero through turned every unset interval into a
		// pool that probes forever, which the byte-comparison caught as a real
		// difference against v1 — the same distinction `sticky` needs a pointer
		// for.
		if p.Interval.Value() > 0 {
			pool.Interval = p.Interval.Value().String()
		}
		if p.IdleTimeout.Value() > 0 {
			pool.IdleTimeout = p.IdleTimeout.Value().String()
		}
		set.Pools[name] = pool
	}
	out.Pools = set

	// The nfqws instances v2 declares are the managed unit, same as v1's.
	names := make([]string, 0, len(cfg.Desync.Instances))
	for name := range cfg.Desync.Instances {
		names = append(names, name)
	}
	sort.Strings(names) // map order must not reach a generated config
	// Position is the alphabetical order, deliberately: it decides the queue
	// numbers, and a queue that CHANGES because a map iterated differently would
	// move an installed instance's traffic to another queue without anyone asking.
	for i, name := range names {
		inst := cfg.Desync.Instances[name]
		// zapret.Instance is the MANAGED unit — a process, a queue, a port set —
		// and it carries no engine field: the engine is chosen by the adapter that
		// launches it, not by the instance record. v2's engine declaration has no
		// home here yet, so it is reported as a gap rather than dropped.
		//
		// The queue number is DERIVED from the instance's position, using the same
		// helper the v1 parser uses. Leaving it at 0 was caught by the live cutover:
		// the allocator refuses a production queue of 0, so the daemon refused to
		// start at all rather than running with an unusable queue.
		out.Zapret = append(out.Zapret, zapret.Instance{
			Name:      name,
			QNum:      DeriveQNum(i),
			Capture:   zapret.Capture{TCP: inst.Capture.TCP, UDP: inst.Capture.UDP},
			Connbytes: inst.Connbytes,
		})
	}

	// A disabled rule is dropped from the registry entirely, so nothing probes,
	// routes or desyncs it. The name is carried separately only so a UI can list
	// it and offer to switch it back on.
	// Bypassed rules are carried SEPARATELY from disabled ones, though both are
	// absent from the runtime. The distinction is the point of §4: `enabled: false`
	// is a switch the operator turned off, `bypass: true` is a standing statement
	// that these domains are not Lotsman's business, and a UI that offers to switch
	// one back on must not offer it for the other.
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

	gaps := make([]string, 0, len(Gaps))
	gaps = append(gaps, Gaps...)
	return out, gaps, nil
}
