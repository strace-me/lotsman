package configv2

import (
	"fmt"
	"strings"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// BuildRegistry turns a v2 configuration and its rules into the registry the
// runtime already consumes.
//
// v2 is a CONFIG LANGUAGE, not a second runtime: the Brain, the Applier, the
// executors and the nft renderer all work on registry.Service, and rebuilding them
// against a new shape would be the large, risky change. The boundary is here
// instead — one function, and everything downstream is untouched. The cutover is
// then a matter of which parser feeds New(), not of rewriting the data plane.
//
// A named list is carried as a RuleSet tag "list:<kind>/<name>" rather than being
// inlined. Two rules may legitimately use the same list, and inlining would copy it
// per rule; worse, a name collision between a list and a sing-box rule-set would
// then be indistinguishable from a real match.
const listTagPrefix = "list:"

// BuildRegistry converts v2 rules into registry services.
func BuildRegistry(cfg *Configuration, rules []Rule, domains, ips map[string]listValues) (*registry.Registry, error) {
	reg := &registry.Registry{Services: map[string]registry.Service{}}
	for _, rule := range rules {
		// Neither a disabled nor a bypassed rule reaches the runtime. They are
		// different facts and are carried separately for that reason — `enabled` is a
		// switch the operator turned off, `bypass` is a standing statement that these
		// domains are not Lotsman's business — but the effect on the registry is the
		// same, and it is the same effect v1 has for a disabled service: no consumer
		// needs a guard, so nothing can forget one.
		if !rule.Enabled || rule.Bypass {
			continue
		}
		service, err := buildService(cfg, rule, domains, ips)
		if err != nil {
			return nil, err
		}
		reg.Services[rule.ID] = *service
	}
	return reg, nil
}

type listValues []string

func buildService(cfg *Configuration, rule Rule, domains, ips map[string]listValues) (*registry.Service, error) {
	service := &registry.Service{
		Name:     rule.ID,
		Priority: rule.Priority,
		Domains:  append([]string{}, rule.Match.Domains...),
		IPs:      append([]string{}, rule.Match.CIDRs...),
	}
	// A named list resolves to a sing-box rule-set TAG, and the tag is what the
	// generator keys on: `geosite-x` means rule-set-geosite/x.srs, `geoip-x` means
	// rule-set-geoip/x.srs.
	//
	// Namespacing the list name instead ("list:domain/x") was the first
	// implementation here and it is wrong. The generator maps a tag to a PATH BY ITS
	// PREFIX and deliberately skips anything unrecognised, so a namespaced name
	// yields a routing rule pointing at a rule-set nothing defines: the config
	// passes `sing-box check` (which resolves rule-sets lazily, on first match) and
	// matches nothing at run time, so every listed rule falls through silently.
	// Comparing a v1-generated config against a v2-generated one byte for byte is
	// what made that visible — no test would have.
	//
	// Resolving through the list's DATASET is also what keeps names apart: a domain
	// list "ru-blocked" and an ip list "ru-blocked" become geosite-ru-blocked and
	// geoip-ru-blocked — two different files, not one name used twice.
	for _, name := range rule.Match.DomainLists {
		tag, err := resolveList(cfg, name, "geosite")
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		service.RuleSets = append(service.RuleSets, tag)
	}
	for _, name := range rule.Match.IPLists {
		tag, err := resolveList(cfg, name, "geoip")
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		service.RuleSets = append(service.RuleSets, tag)
	}
	if rule.Probe != nil {
		service.ProbeType = rule.Probe.Type
		service.ProbeTarget = rule.Probe.Target
	}
	// The behaviour knobs carry across as they are. Two of them need care, and the
	// care is the point: `sticky` is a POINTER in v1 so that unset (take the
	// default) is distinct from false (override a default of true), and a plain bool
	// here would turn every rule that says nothing into a rule that asks to flap.
	if rule.Behavior.Sticky != nil {
		service.Sticky = *rule.Behavior.Sticky
	}
	service.Profile = rule.Behavior.Profile
	service.SpreadClients = append([]string{}, rule.Behavior.SpreadClients...)
	service.EscalateAfter = rule.Behavior.EscalateAfter
	service.RecoverAfter = rule.Behavior.RecoverAfter
	service.TLSFragment = rule.Behavior.TLSFragment
	// ExcludeDomains is deliberately NOT set. In v1 it was a per-service list fed
	// to the composer as --hostlist-exclude-domains. v2 carries no such field: an
	// exclusion is a property of the STRATEGY BUNDLE, which declares its own
	// --hostlist-exclude, because the exclusion exists to survive the moment the
	// strategy is applied and the rule is the only thing that applies it. Leaving
	// it empty here keeps the rule from re-asserting something the bundle already
	// owns.
	if rule.Volume != nil {
		service.VolumeTarget, service.VolumeTargets = volumeTargets(rule.Volume)
		service.VolumeBytes = int(rule.Volume.MinBytes)
	}
	steps, err := buildChain(rule, cfg)
	if err != nil {
		return nil, err
	}
	service.Chain = steps
	return service, nil
}

// volumeTargets splits the v2 volume into the primary target and the additional
// ones, without inventing a primary out of thin air: the FIRST named target is the
// primary, because the canary judges the WORST endpoint and an empty primary would
// quietly become the probe target — a 204 with no body, which is how a throughput
// measurement once read 0 KiB/s and demoted a working strategy.
func volumeTargets(volume *Volume) (string, []string) {
	if len(volume.Targets) == 0 {
		return "", nil
	}
	rest := make([]string, 0, len(volume.Targets)-1)
	for _, target := range volume.Targets[1:] {
		if target != volume.Targets[0] {
			rest = append(rest, target)
		}
	}
	return volume.Targets[0], rest
}

func buildChain(rule Rule, cfg *Configuration) ([]registry.ChainStep, error) {
	steps := make([]registry.ChainStep, 0, len(rule.Chain))
	for i, step := range rule.Chain {
		out := registry.ChainStep{Position: i}
		switch {
		case step.Action.Desync != nil:
			out.StrategyClass = strategy.ClassZapret
			out.StrategyID = step.Action.Desync.Strategy
			out.State = tierState(step.Tier, i == 0)
		case step.Action.VPN != nil:
			out.StrategyClass = strategy.ClassVPN
			out.StrategyID = step.Action.VPN.Pool
			out.State = tierState(step.Tier, false)
			out.Detour = step.Action.VPN.Detour
		case step.Action.Direct:
			out.StrategyClass = strategy.ClassDirect
			out.StrategyID = "direct"
			out.State = tierState(step.Tier, false)

		default:
			return nil, fmt.Errorf("rule %q: step %d has no action", rule.ID, i)
		}
		steps = append(steps, out)
	}
	return steps, nil
}

// tierState maps the v2 tier label onto the v1 state name the status surface and
// the Brain's logs read. The LABEL is a grouping for the operator; the state is
// the position in the ladder, so the first rung is PREFERRED and everything after
// it takes the tier's own name. The old states ALT_ZAPRET and LOCKED disappear
// here on purpose: both were labels, and v2 expresses the same thing with a tier
// plus a strategy.
func tierState(tier string, first bool) string {
	switch {
	case first:
		return registry.StatePreferred
	case tier == "fallback":
		return registry.StateVPN
	case tier == "emergency":
		return registry.StateEmergency
	case tier == "preferred":
		// A second rung in the preferred tier: the same "try desync, differently"
		// meaning v1 spelled ALT_ZAPRET.
		return registry.StateAltZapret
	default:
		return registry.StateVPN
	}
}

// resolveList turns a v2 list NAME into the sing-box rule-set tag the generator
// understands, checking the declared dataset against the rule's own namespace.
// ResolveListTag returns the rule-set tag a list name resolves to, with the same
// dataset-aware naming the runtime uses.
//
// It exists so that callers outside the bridge — the v2 preflight, which has to know
// which files to materialize before it can check anything — ask the runtime's question
// instead of reimplementing it. A second copy of this resolution is a second answer:
// when the two disagreed, the preflight checked for rule-sets the daemon would never
// look for and reported every list as broken while the config was fine.
func ResolveListTag(cfg *Configuration, name, wantPrefix string) (string, error) {
	return resolveList(cfg, name, wantPrefix)
}

func resolveList(cfg *Configuration, name, wantPrefix string) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("list %q cannot be resolved without a configuration", name)
	}
	var list List
	if wantPrefix == "geoip" {
		found, ok := cfg.Lists.IPs[name]
		if !ok {
			return "", fmt.Errorf("ip list %q is not declared", name)
		}
		list = found
	} else {
		found, ok := cfg.Lists.Domains[name]
		if !ok {
			return "", fmt.Errorf("domain list %q is not declared", name)
		}
		list = found
	}
	// A domain rule-set in the ip namespace, or the reverse, means the rule names
	// the wrong kind of list. Silently accepting it would route by the wrong
	// vocabulary — an IP list matching domains is not a partial match, it is a rule
	// that can never fire.
	if list.Dataset != "" && !strings.EqualFold(list.Dataset, wantPrefix) {
		return "", fmt.Errorf("list %q is a %s list but is used where a %s list is required", name, list.Dataset, wantPrefix)
	}
	category := list.Category
	if category == "" {
		category = name
	}
	return wantPrefix + "-" + category, nil
}
