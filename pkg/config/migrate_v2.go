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

func MigrateToV2(doc *Document, desyncStrategy DesyncStrategyFor) (*V2Migration, error) {
	m := &V2Migration{Configuration: &configv2.Configuration{Version: 2}}

	for _, instance := range doc.Zapret.Instances {
		if m.Configuration.Desync.Instances == nil {
			m.Configuration.Desync.Instances = map[string]configv2.Instance{}
		}
		m.Configuration.Desync.Instances[Slug(instance.Name)] = configv2.Instance{Engine: "nfqws1", Enabled: true}
	}

	m.Configuration.Pools = map[string]configv2.Pool{}
	for name, pool := range doc.Pools {
		m.Configuration.Pools[Slug(name)] = configv2.Pool{
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
			Format:  string(sub.Format),
			Tags:    sub.Tags,
			Enabled: sub.Enabled,
			Expires: sub.Expires,
		})
	}

	m.Configuration.Lists.Domains = map[string]configv2.List{}
	m.Configuration.Lists.IPs = map[string]configv2.List{}
	for _, hostlist := range doc.Hostlists {
		m.Configuration.Lists.Domains[Slug(hostlist.Name)] = configv2.List{
			Files:   nonEmpty([]string{hostlist.Out}),
			URLs:    hostlist.Sources,
			Domains: hostlist.Domains,
		}
	}

	instance, err := soleDesyncInstance(m.Configuration)
	if err != nil {
		return nil, err
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

func migrateService(service serviceYAML, cfg *configv2.Configuration, instance string, desyncStrategy DesyncStrategyFor) (*configv2.Rule, []string, error) {
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
	}
	if service.ProbeType != "" {
		rule.Probe = &configv2.Probe{Type: service.ProbeType, Target: service.ProbeTarget}
	}
	if targets := nonEmpty(append([]string{service.VolumeTarget}, service.VolumeTargets...)); len(targets) > 0 || service.VolumeBytes > 0 {
		rule.Volume = &configv2.Volume{Targets: targets, MinBytes: int64(service.VolumeBytes)}
	}
	if service.IPsFile != "" {
		listName := id + "-ips"
		if cfg.Lists.IPs == nil {
			cfg.Lists.IPs = map[string]configv2.List{}
		}
		cfg.Lists.IPs[listName] = configv2.List{Files: []string{service.IPsFile}}
		rule.Match.IPLists = append(rule.Match.IPLists, listName)
	}
	for _, tag := range service.RuleSets {
		listName, ok := ruleSetToList(tag)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("rule %q: rule_set %q is not a geosite/geoip category and was dropped", id, tag))
			continue
		}
		if _, seen := cfg.Lists.Domains[listName]; !seen {
			cfg.Lists.Domains[listName] = configv2.List{Source: "runetfreedom", Dataset: datasetFor(tag), Category: categoryFor(tag)}
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
	if service.Sticky != nil || len(service.SpreadClients) > 0 || service.TLSFragment {
		warnings = append(warnings, fmt.Sprintf("rule %q: sticky/spread_clients/tls_fragment have no v2 rule field yet", id))
	}
	if service.Static || service.EscalateAfter > 0 || service.RecoverAfter > 0 {
		warnings = append(warnings, fmt.Sprintf("rule %q: static/escalate_after/recover_after have no v2 rule field yet", id))
	}
	return rule, warnings, nil
}

func migrateChain(ruleID string, steps []chainStepYAML, instance string, desyncStrategy DesyncStrategyFor) ([]configv2.ChainStep, []string, error) {
	var out []configv2.ChainStep
	var warnings []string
	seenTier := map[string]bool{}
	for _, step := range steps {
		tier, ok := tierFor(step.State)
		if !ok {
			if step.State == registryStateBroken {
				warnings = append(warnings, fmt.Sprintf("rule %q: chain state %s is runtime-only and was dropped", ruleID, step.State))
				continue
			}
			return nil, nil, fmt.Errorf("rule %q: unknown chain state %q", ruleID, step.State)
		}
		if seenTier[tier] {
			return nil, nil, fmt.Errorf("rule %q: chain repeats tier %q after migration; v2 allows one step per tier", ruleID, tier)
		}
		seenTier[tier] = true
		switch step.Class {
		case strategy.ClassDirect:
			out = append(out, configv2.ChainStep{Tier: tier, Action: configv2.Action{Direct: true}})
		case strategy.ClassVPN, strategy.ClassEmergency:
			if step.StrategyID == "" {
				return nil, nil, fmt.Errorf("rule %q: class %q without strategy_id cannot name a pool", ruleID, step.Class)
			}
			out = append(out, configv2.ChainStep{Tier: tier, Action: configv2.Action{VPN: &configv2.VPNAction{Pool: Slug(step.StrategyID)}}})
		case strategy.ClassZapret:
			bundle := step.StrategyID
			if bundle == "" && desyncStrategy != nil {
				resolved, err := desyncStrategy(instance)
				if err != nil {
					return nil, nil, fmt.Errorf("rule %q: %w", ruleID, err)
				}
				bundle = resolved
			}
			if bundle == "" {
				return nil, nil, fmt.Errorf("rule %q: class zapret needs a strategy bundle; supply DesyncStrategyFor", ruleID)
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

func soleDesyncInstance(cfg *configv2.Configuration) (string, error) {
	if len(cfg.Desync.Instances) == 0 {
		// v1 allowed a config with zapret rungs and no declared instance — the
		// single implicit nfqws1 process. v2 names instances, so materialise it.
		cfg.Desync.Instances = map[string]configv2.Instance{"nfqws1-main": {Engine: "nfqws1", Enabled: true}}
		return "nfqws1-main", nil
	}
	if len(cfg.Desync.Instances) != 1 {
		names := make([]string, 0, len(cfg.Desync.Instances))
		for name := range cfg.Desync.Instances {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("migration needs exactly one desync instance, found %d %v", len(names), names)
	}
	for name := range cfg.Desync.Instances {
		return name, nil
	}
	return "", nil
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
