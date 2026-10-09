package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// V2Migration is the result of reading a v1 document as v2. Anything v1
// expressed that v2 has no place for is reported in Warnings instead of being
// silently dropped or guessed at.
type V2Migration struct {
	Configuration *configv2.Configuration
	Rules         []configv2.Rule
	Warnings      []string
}

// DesyncStrategyFor resolves which strategy bundle a v1 `class: zapret` rung
// means. v1 kept one preset per instance, so the rung named no strategy; v2
// requires an explicit {instance, strategy} pair. The caller owns that policy,
// so an ambiguous install is reported by the caller, not guessed at here.
type DesyncStrategyFor func(instance string) (string, error)

var errChainEmpty = errors.New("chain is empty after migration")

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(name string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

func MigrateToV2(doc *LegacyDocument, desyncStrategy DesyncStrategyFor) (*V2Migration, error) {
	m := &V2Migration{Configuration: &configv2.Configuration{Version: 2, TargetVersion: doc.SingboxVersion}}
	if doc.SingboxVersion == "" {
		// Worth a warning rather than a shrug: with no target the generator runs at
		// its 1.12.17 baseline, and a node carrying a version-gated feature (hy2
		// gecko obfs, for one) is emitted WITHOUT it. That is a silent behaviour
		// change on a working config.
		m.Warnings = append(m.Warnings, "no singbox_version: the generated config targets the generator baseline, so version-gated node features (e.g. hy2 gecko obfs) will be stripped")
	}

	// The engine knobs carry. Each of these was a v1 section the first v2 draft had no
	// field for, and a migration that dropped one would look successful while quietly
	// running at a default the operator did not choose — the failure mode this whole
	// converter exists to avoid.
	m.Configuration.UTLSFingerprint = doc.UTLSFingerprint
	m.Configuration.SubViaPool = doc.SubViaPool
	if doc.DNS != nil {
		m.Configuration.DNS = &configv2.DNSSection{
			Servers:        doc.DNS.Servers,
			Direct:         doc.DNS.Direct,
			Zapret:         doc.DNS.Zapret,
			Final:          doc.DNS.Final,
			Strategy:       doc.DNS.Strategy,
			Failover:       doc.DNS.Failover,
			DirectFailover: doc.DNS.DirectFailover,
			FakeIP:         doc.DNS.FakeIP,
		}
		if len(doc.DNS.Servers) > 0 {
			m.Warnings = append(m.Warnings, "the split-DNS block carried over, including its failover lists — the resolvability of a resolver is the host's, so check the new one answers before relying on the rotation")
		}
	}
	// The LEGACY standalone fakeip block has no v2 spelling, because fakeip is a property
	// of the resolver set rather than a document section of its own. It folds into
	// dns.fakeip — and the ranges do NOT come with it, so that is said plainly: a
	// converted document that quietly used different fake ranges would answer from
	// addresses the operator never chose.
	if f := doc.FakeIP; f != nil && f.Enabled {
		if m.Configuration.DNS == nil {
			m.Configuration.DNS = &configv2.DNSSection{}
		}
		m.Configuration.DNS.FakeIP = true
		m.Warnings = append(m.Warnings, fmt.Sprintf(
			"the standalone fakeip block became dns.fakeip: true. The RESOLVER follows the generated dns block, and the ranges are the standard reserved ones"+
				" — your inet4_range %q / inet6_range %q / resolver %q are not carried",
			f.Inet4Range, f.Inet6Range, f.Resolver))
	}
	if mx := doc.Multiplex; mx != nil && mx.Enabled {
		m.Configuration.Multiplex = &configv2.Multiplex{
			Enabled:        true,
			Protocol:       mx.Protocol,
			MaxConnections: mx.MaxConnections,
			MinStreams:     mx.MinStreams,
			Padding:        mx.Padding,
			BrutalUp:       mx.BrutalUp,
			BrutalDown:     mx.BrutalDown,
		}
	}
	for _, st := range doc.Strategies {
		m.Configuration.Strategies = append(m.Configuration.Strategies, configv2.Strategy{
			ID: st.ID, Class: st.Class, NFQWSArgs: st.NFQWSArgs,
			BlockTypes: st.BlockTypes, Notes: st.Notes,
		})
	}
	for _, d := range doc.Devices {
		m.Configuration.Devices = append(m.Configuration.Devices, configv2.Device{
			Name: d.Name, Sources: d.Sources, Policy: d.Policy,
		})
	}
	if len(doc.Categories) > 0 {
		m.Warnings = append(m.Warnings, fmt.Sprintf(
			"the %d `categories:` block(s) have no v2 equivalent: behaviour profiles were removed, and every rule now states its own ladder, sticky flag and profile. "+
				"The rules migrated with the chain they RESOLVED to, not the category they inherited it from — check a category's rules", len(doc.Categories)))
	}

	// The PRESET carries, as an override. It is the one v1 section with no obvious
	// v2 spelling — it is not a list, it is a MODE — and dropping it would look like a
	// successful migration while quietly turning per-rule composition on. That is the
	// worst possible outcome for this particular file: composition refuses
	// `flowseal-alt12-google` as self-shadowing, so the Google rule would lose its
	// recipe and the owner would have no idea why.
	if p := doc.Zapret.Preset; p.Dir != "" || p.ArgsFile != "" {
		m.Configuration.Desync.Override = &configv2.Override{Name: p.Name, ArgsFile: p.ArgsFile, Dir: p.Dir}
		m.Warnings = append(m.Warnings, fmt.Sprintf(
			"the preset (zapret.preset) became desync.override: it is now named as an override rather than a preset, and it still serves EVERY rule. "+
				"Per-rule recipe learning stays off while it is set — one bundle serves all rules, so there is nothing to choose between. "+
				"Note the per-rule `strategy_id` on a desync rung is IGNORED in this mode"))
	}

	for _, instance := range doc.Zapret.Instances {
		if m.Configuration.Desync.Instances == nil {
			m.Configuration.Desync.Instances = map[string]configv2.Instance{}
		}
		// The ENGINE is not carried: v1 has one engine for every instance, and v2
		// names the engine on the instance. Defaulting to nfqws1 preserves what a v1
		// file actually ran, rather than guessing upward to the newer engine — which
		// would be an upgrade disguised as a migration.
		//
		// The CAPTURE set is carried because dropping it is not a default: an instance
		// with no ports installs an nft rule that matches nothing, so the desync is
		// silently absent while every status read stays healthy.
		capture := configv2.Capture{TCP: portList(instance.Capture.TCP), UDP: portList(instance.Capture.UDP)}
		if len(capture.TCP) == 0 && len(capture.UDP) == 0 {
			// v1 let an instance declare no ports and meant "the whole thing"; v2 does
			// not, and the difference is silent in the worst way — an nft rule that
			// matches nothing, a desync engine that is absent, and every status read
			// still reporting the strategy as applied. So the migrated instance is given
			// the port set the desync is FOR and the substitution is announced.
			capture = defaultMigratedCapture
			m.Warnings = append(m.Warnings, fmt.Sprintf(
				"desync instance %q declared no capture: it was given %v. That is what the DPI-desync "+
					"lane is for — change it if your traffic arrives on other ports",
				instance.Name, capture))
		}
		m.Configuration.Desync.Instances[Slug(instance.Name)] = configv2.Instance{
			Engine:    "nfqws1",
			Enabled:   true,
			Capture:   capture,
			Connbytes: instance.Connbytes,
		}
	}

	m.Configuration.Pools = map[string]configv2.Pool{}
	for name, pool := range doc.Pools {
		// NOT slugged. A rule id becomes a file name, so it must be a safe token; a
		// pool is a reference to a name the OPERATOR chose, and rewriting it renames
		// something of theirs without saying so. The migration audit caught this:
		// vpn_url_test_udp came out as vpn-url-test-udp, so every rule's fallback
		// pointed at a name the operator never wrote.
		m.Configuration.Pools[name] = configv2.Pool{
			Type:        pool.Type,
			Caps:        pool.Filter.Caps,
			TagsInclude: pool.Filter.TagsInclude,
			TagsExclude: pool.Filter.TagsExclude,
			Warmup:      pool.Warmup,
			Interval:    configv2.Duration(mustDuration(pool.Interval)),
			IdleTimeout: configv2.Duration(mustDuration(pool.IdleTimeout)),
		}
	}

	for _, sub := range doc.Subscriptions {
		m.Configuration.Subscriptions = append(m.Configuration.Subscriptions, configv2.Subscription{
			Name:    sub.Name,
			URL:     sub.URL,
			Format:  sub.Format,
			Tags:    sub.Tags,
			Enabled: sub.Enabled,
			Expires: sub.Expires,
		})
	}

	m.Configuration.Lists.Domains = map[string]configv2.List{}
	m.Configuration.Lists.IPs = map[string]configv2.List{}
	// A hostlist carries into `hostlists`, NOT into `lists.domains`. The two feed
	// different consumers: a list becomes a sing-box rule-set, a hostlist becomes a
	// plain text file the desync engine re-reads on mtime. Filing it as a list produced
	// a config that ROUTES correctly and DESYNCS NOTHING — the rule matched its
	// rule-set, the composer handed nfqws an empty hostlist, and every status read said
	// the strategy was applied.
	//
	// It also keeps what made a hostlist worth declaring: the exclude sources and the
	// shrink guard, neither of which a rule-set has.
	for _, hostlist := range doc.Hostlists {
		m.Configuration.Hostlists = append(m.Configuration.Hostlists, configv2.Hostlist{
			Name:         hostlist.Name,
			Out:          hostlist.Out,
			Sources:      hostlist.Sources,
			Exclude:      hostlist.Exclude,
			Domains:      hostlist.Domains,
			MinKeepRatio: hostlist.MinKeepRatio,
		})
	}

	instance, implicit, err := soleDesyncInstance(m.Configuration)
	if err != nil {
		return nil, err
	}
	if implicit {
		m.Warnings = append(m.Warnings, fmt.Sprintf(
			"no desync instance was declared, so one was materialised (%q) carrying %v. "+
				"That is what the DPI-desync lane is for — declare an instance with the ports your "+
				"traffic actually arrives on if it is not these", instance, defaultMigratedCapture))
	}
	for _, service := range doc.Services {
		rule, warnings, err := migrateService(service, m.Configuration, instance, desyncStrategy)
		if errors.Is(err, errChainEmpty) {
			m.Warnings = append(m.Warnings, fmt.Sprintf("service %q was dropped: %v", service.Name, err))
			continue
		}
		if err != nil {
			return nil, err
		}
		m.Rules = append(m.Rules, *rule)
		m.Warnings = append(m.Warnings, warnings...)
	}

	sort.SliceStable(m.Rules, func(i, j int) bool { return m.Rules[i].Priority > m.Rules[j].Priority })
	if usesRunetfreedom(m.Configuration.Lists) {
		if m.Configuration.DataSources == nil {
			m.Configuration.DataSources = map[string]configv2.DataSource{}
		}
		if _, declared := m.Configuration.DataSources[runetfreedomSource]; !declared {
			m.Configuration.DataSources[runetfreedomSource] = configv2.DataSource{
				Type:    "v2ray-srs",
				Repo:    "runetfreedom/russia-v2ray-rules-dat",
				Refresh: configv2.Duration(6 * time.Hour),
			}
			m.Warnings = append(m.Warnings, "declared the runetfreedom data source: v1 rule_sets resolved to a .srs bundle, pin the repo if you track a fork")
		}
	}
	if err := m.Configuration.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func migrateService(service LegacyService, cfg *configv2.Configuration, instance string, desyncStrategy DesyncStrategyFor) (*configv2.Rule, []string, error) {
	var warnings []string
	id := Slug(service.Name)
	rule := &configv2.Rule{
		ID:       id,
		Name:     service.Name,
		Enabled:  !service.Disabled,
		Priority: service.Priority,
		Match: configv2.Match{
			Domains:     service.Domains,
			DomainLists: service.DomainLists,
			CIDRs:       service.IPs,
		},
		// The behaviour knobs MOVE, rather than being reported as lost. Sticky
		// stays a *bool on the way in because the distinction is the whole point:
		// unset means "inherit the default" and false means "override a default
		// of true", and a plain bool would silently turn every rule that said
		// nothing into one that asks to flap by latency.
		Behavior: configv2.Behavior{
			Sticky:        service.Sticky,
			Profile:       service.Profile,
			SpreadClients: service.SpreadClients,
			EscalateAfter: service.EscalateAfter,
			RecoverAfter:  service.RecoverAfter,
			TLSFragment:   service.TLSFragment,
		},
	}
	// The TARGET is what carries, and the type only qualifies it. Gating on the type
	// dropped the target of every rule that relied on the default — which is most of
	// them — leaving a rule with no probe at all: nothing to check it against, no volume
	// verdict, and a ladder that cannot tell working from frozen.
	if service.ProbeType != "" || service.ProbeTarget != "" {
		probeType := service.ProbeType
		if probeType == "" {
			// http is the schema's own default for an unstated type, so writing it
			// explicitly changes nothing and makes the migrated file say what it means.
			probeType = "http"
		}
		rule.Probe = &configv2.Probe{Type: probeType, Target: service.ProbeTarget}
	}
	if targets := nonEmpty(append([]string{service.VolumeTarget}, service.VolumeTargets...)); len(targets) > 0 || service.VolumeBytes > 0 {
		rule.Volume = &configv2.Volume{Targets: targets, MinBytes: int64(service.VolumeBytes)}
	}
	if service.IPsFile != "" {
		// v1 READS this file at parse time and merges the CIDRs straight into the
		// rule, so they become part of the service's own match. Migrating it to a
		// named IP list instead turned the same intent into a rule-set: a different
		// mechanism, which the byte-comparison showed as an EXTRA rule-set on a rule
		// v1 emitted with only its geosite list — and as a config that fails
		// `sing-box check` on a .srs nobody ships.
		cidrs, err := loadIPsFile(id, service.IPsFile)
		if err != nil {
			return nil, nil, err
		}
		rule.Match.CIDRs = append(rule.Match.CIDRs, cidrs...)
		warnings = append(warnings, fmt.Sprintf(
			"rule %q: ips_file %q is now INLINED as CIDRs, which is what v1 did at parse time; the file is no longer referenced by name",
			id, service.IPsFile))
	}
	for _, tag := range service.RuleSets {
		listName, ok := ruleSetToList(tag)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("rule %q: rule_set %q is not a geosite/geoip category and was dropped", id, tag))
			continue
		}
		// A geoip rule-set is an IP list and a geosite one is a domain list. Filing
		// both under one name collapsed them: with both geosite-ru-blocked and
		// geoip-ru-blocked in play, the second definition was skipped as "already
		// seen" and the same name landed in DomainLists twice — so the config claimed
		// a domain list where an IP list belonged, and the byte-comparison against v1
		// is what made it visible.
		list := configv2.List{Source: "runetfreedom", Dataset: datasetFor(tag), Category: categoryFor(tag)}
		if datasetFor(tag) == "geoip" {
			if cfg.Lists.IPs == nil {
				cfg.Lists.IPs = map[string]configv2.List{}
			}
			if _, seen := cfg.Lists.IPs[listName]; !seen {
				cfg.Lists.IPs[listName] = list
			}
			rule.Match.IPLists = append(rule.Match.IPLists, listName)
			continue
		}
		if cfg.Lists.Domains == nil {
			cfg.Lists.Domains = map[string]configv2.List{}
		}
		if _, seen := cfg.Lists.Domains[listName]; !seen {
			cfg.Lists.Domains[listName] = list
		}
		rule.Match.DomainLists = append(rule.Match.DomainLists, listName)
	}

	steps, stepWarnings, err := migrateChain(id, service.Chain, instance, desyncStrategy)
	if err != nil {
		return nil, nil, err
	}
	rule.Chain = steps
	warnings = append(warnings, stepWarnings...)

	if service.Category != "" {
		warnings = append(warnings, fmt.Sprintf("rule %q: category %q has no v2 equivalent (behaviour profiles were removed)", id, service.Category))
	}
	if len(service.ExcludeLists) > 0 || len(service.ExcludeDomains) > 0 {
		warnings = append(warnings, fmt.Sprintf("rule %q: exclude lists/domains have no v2 rule field yet", id))
	}
	// `static` is the one that genuinely has no v2 home: v1 nailed a service to its
	// configured chain[0] and the daemon never escalated or recovered it. That is a
	// PROPERTY OF THE LADDER, and v2 states the ladder step by step instead, so the
	// closest honest statement is a single-rung chain — which is what a static
	// service's chain reduces to anyway. Everything else in this group now moves.
	if service.Static {
		warnings = append(warnings, fmt.Sprintf("rule %q: `static` has no v2 field; express it as a single-rung chain, which is what a static service reduces to", id))
	}
	return rule, warnings, nil
}

func migrateChain(ruleID string, steps []LegacyChainStep, instance string, desyncStrategy DesyncStrategyFor) ([]configv2.ChainStep, []string, error) {
	var out []configv2.ChainStep
	var warnings []string
	// The tier LABEL is clamped to be non-decreasing along the chain.
	//
	// v1 let a service put its rungs in any order: discord runs VPN, then a
	// PREFERRED desync rung, then VPN again. Nothing depended on that — the
	// daemon escalates by POSITION, so `state` was a label an operator wrote and
	// nothing read. v2 forbids a tier going backwards, and that rule is right for
	// a document a human is about to edit, but applying it to a migration would
	// reject the operator's own working file.
	//
	// So the order is preserved exactly — that is the behaviour — and only the
	// label is clamped, with a warning. Dropping or reordering a rung would change
	// what the service does; clamping a label cannot.
	highest := -1
	for _, step := range steps {
		tier, ok := tierFor(step.State)
		if !ok {
			if step.State == registryStateBroken {
				warnings = append(warnings, fmt.Sprintf("rule %q: chain state %s is runtime-only and was dropped", ruleID, step.State))
				continue
			}
			return nil, nil, fmt.Errorf("rule %q: unknown chain state %q", ruleID, step.State)
		}
		if rank := tierRank(tier); rank < highest {
			warnings = append(warnings, fmt.Sprintf(
				"rule %q: the %q rung sits after a %q rung; v2 forbids a tier going backwards, so the LABEL was clamped — the position is unchanged, and position is what the runtime follows",
				ruleID, tier, configv2.ChainTiers[highest]))
			tier = configv2.ChainTiers[highest]
		} else {
			highest = tierRank(tier)
		}
		switch step.Class {
		case strategy.ClassDirect:
			out = append(out, configv2.ChainStep{Tier: tier, Action: configv2.Action{Direct: true}})
		case strategy.ClassVPN, strategy.ClassEmergency:
			if step.StrategyID == "" {
				return nil, nil, fmt.Errorf("rule %q: class %q without strategy_id cannot name a pool", ruleID, step.Class)
			}
			if step.Class == strategy.ClassEmergency {
				// v1 had an `emergency` class with NO executor, so a rule that
				// reached this rung failed with "no executor for class" — the
				// incident CHANGELOG records. v2 carries the pool on the vpn action,
				// which makes the rung reachable. That is a fix, and it IS a change
				// of behaviour, so it is reported rather than discovered later as a
				// rule that suddenly routes somewhere it never did.
				warnings = append(warnings, fmt.Sprintf(
					"rule %q: the emergency rung %q had no executor in v1 and is dead; it now runs as a vpn pool",
					ruleID, step.StrategyID))
			}
			out = append(out, configv2.ChainStep{Tier: tier, Action: configv2.Action{VPN: &configv2.VPNAction{Pool: step.StrategyID}}})
		case strategy.ClassZapret:
			// An empty strategy_id in v1 meant "the KB picks" — ALT_ZAPRET's whole
			// purpose. v2 says that out loud as `auto` rather than leaving the rung
			// looking like a pinned strategy it never was.
			bundle := "auto"
			if step.StrategyID != "" {
				bundle = step.StrategyID
			} else if desyncStrategy != nil {
				resolved, err := desyncStrategy(instance)
				if err != nil {
					return nil, nil, fmt.Errorf("rule %q: %w", ruleID, err)
				}
				if resolved != "" {
					bundle = resolved
				}
			}
			out = append(out, configv2.ChainStep{Tier: tier, Action: configv2.Action{Desync: &configv2.DesyncAction{Instance: instance, Strategy: bundle}}})
		case strategy.ClassByeDPI:
			warnings = append(warnings, fmt.Sprintf("rule %q: class byedpi is not a v2 chain action and was dropped", ruleID))
		default:
			return nil, nil, fmt.Errorf("rule %q: unknown chain class %q", ruleID, step.Class)
		}
	}
	if len(out) == 0 {
		return nil, nil, errChainEmpty
	}
	return out, warnings, nil
}

// defaultMigratedCapture is the port set a v1 instance with no declared capture gets.
// 80/443 is what a browser speaks; 443 on UDP is QUIC, which is what a lot of clients try
// first and what the censor most often blocks. It is a DEFAULT, and every use of it is
// announced — a migration that quietly picked different ports would be changing what the
// desync covers without saying so.
var defaultMigratedCapture = configv2.Capture{TCP: []string{"80", "443"}, UDP: []string{"443"}}

const runetfreedomSource = "runetfreedom"

func usesRunetfreedom(lists configv2.Lists) bool {
	for _, group := range []map[string]configv2.List{lists.Domains, lists.IPs} {
		for _, list := range group {
			if list.Source == runetfreedomSource {
				return true
			}
		}
	}
	return false
}

const registryStateBroken = "BROKEN"

// soleDesyncInstance names the one instance a migrated document routes through, and
// reports whether it had to be materialised — which is a fact the operator should be
// told, because the port set was then chosen by this code rather than by them.
func soleDesyncInstance(cfg *configv2.Configuration) (name string, implicit bool, err error) {
	if len(cfg.Desync.Instances) == 0 {
		// v1 allowed a config with zapret rungs and no declared instance — the single
		// implicit nfqws1 process, whose port set came from a launcher script rather
		// than from the document. v2 names instances AND requires their ports, so the
		// materialised one carries the default set.
		//
		// Without it the instance would be an nft rule matching nothing: the desync
		// engine absent, and every status read still reporting the strategy applied.
		cfg.Desync.Instances = map[string]configv2.Instance{
			"nfqws1-main": {Engine: "nfqws1", Enabled: true, Capture: defaultMigratedCapture},
		}
		return "nfqws1-main", true, nil
	}
	if len(cfg.Desync.Instances) != 1 {
		names := make([]string, 0, len(cfg.Desync.Instances))
		for name := range cfg.Desync.Instances {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", false, fmt.Errorf("migration needs exactly one desync instance, found %d %v", len(names), names)
	}
	for name := range cfg.Desync.Instances {
		return name, false, nil
	}
	return "", false, nil
}

// tierRank orders the tier labels. It reads configv2.ChainTiers rather than
// restating them, so a new tier cannot be added to the schema and missed here.
func tierRank(tier string) int {
	for i, t := range configv2.ChainTiers {
		if t == tier {
			return i
		}
	}
	return -1
}

func tierFor(state string) (string, bool) {
	switch state {
	case "PREFERRED", "ALT_ZAPRET":
		return "preferred", true
	case "VPN":
		return "fallback", true
	case "EMERGENCY", "LOCKED":
		return "emergency", true
	}
	return "", false
}

func ruleSetToList(tag string) (string, bool) {
	name := categoryFor(tag)
	if name == "" {
		return "", false
	}
	return name, true
}

func datasetFor(tag string) string {
	if strings.HasPrefix(tag, "geoip-") {
		return "geoip"
	}
	return "geosite"
}

func categoryFor(tag string) string {
	for _, prefix := range []string{"geosite-", "geoip-"} {
		if strings.HasPrefix(tag, prefix) {
			return Slug(strings.TrimPrefix(tag, prefix))
		}
	}
	return ""
}

func nonEmpty(in []string) []string {
	var out []string
	for _, value := range in {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func mustDuration(raw string) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return parsed
}
