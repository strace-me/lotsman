package configv2

import (
	"fmt"

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

// ListTag is the RuleSet tag standing for one named v2 list.
func ListTag(kind, name string) string {
	if kind == "ips" {
		return listTagPrefix + "ip/" + name
	}
	return listTagPrefix + "domain/" + name
}

// ParseListTag splits a tag produced by ListTag, reporting whether it was one.
func ParseListTag(tag string) (kind, name string, ok bool) {
	rest, ok := cutPrefix(tag, listTagPrefix)
	if !ok {
		return "", "", false
	}
	kind, name, found := cut(rest, "/")
	if !found {
		return "", "", false
	}
	return kind, name, true
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}

func cut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// BuildRegistry converts v2 rules into registry services.
func BuildRegistry(cfg *Configuration, rules []Rule, domains, ips map[string]listValues) (*registry.Registry, error) {
	reg := &registry.Registry{Services: map[string]registry.Service{}}
	for _, rule := range rules {
		if !rule.Enabled {
			// v1 drops a disabled service out of buildRegistry entirely, so that no
			// consumer needs a guard for it. Matching that is what keeps a disabled
			// rule from being probed because one consumer forgot to check.
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
		RuleSets: append([]string{}, rule.Match.DomainLists...),
		Domains:  append([]string{}, rule.Match.Domains...),
		IPs:      append([]string{}, rule.Match.CIDRs...),
	}
	for i, tag := range service.RuleSets {
		service.RuleSets[i] = ListTag("domains", tag)
	}
	for i, tag := range rule.Match.IPLists {
		service.RuleSets = append(service.RuleSets, ListTag("ips", tag))
		_ = i
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
		case step.Action.Direct:
			out.StrategyClass = strategy.ClassDirect
			out.StrategyID = "direct"
			out.State = tierState(step.Tier, false)
		case step.Action.Bypass:
			// v1 has no bypass rung: the nearest thing is a service switched off.
			// Carrying it as "direct" would assert something false — that traffic is
			// routed there on purpose — so it is refused rather than mistranslated.
			return nil, fmt.Errorf("rule %q: bypass at step %d has no v1 equivalent; it is a config concept, not a route", rule.ID, i)
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
