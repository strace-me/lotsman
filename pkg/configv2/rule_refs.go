package configv2

import (
	"fmt"
)

// ValidateReferences checks a rule against the loaded configuration: every
// named list, pool, desync instance and strategy it references must exist.
// strategies maps a desync instance name to the strategy ids its store holds.
func (r *Rule) ValidateReferences(cfg *Configuration, strategies map[string][]string) error {
	if len(r.Match.Domains) == 0 && len(r.Match.DomainLists) == 0 && len(r.Match.IPLists) == 0 && len(r.Match.CIDRs) == 0 {
		return fmt.Errorf("rule %q has an empty match", r.ID)
	}
	for _, name := range r.Match.DomainLists {
		if _, ok := cfg.Lists.Domains[name]; !ok {
			return fmt.Errorf("rule %q references unknown domain list %q", r.ID, name)
		}
	}
	for _, name := range r.Match.IPLists {
		if _, ok := cfg.Lists.IPs[name]; !ok {
			return fmt.Errorf("rule %q references unknown ip list %q", r.ID, name)
		}
	}
	for _, step := range r.Chain {
		if desync := step.Action.Desync; desync != nil {
			instance, ok := cfg.Desync.Instances[desync.Instance]
			if !ok {
				return fmt.Errorf("rule %q references unknown desync instance %q", r.ID, desync.Instance)
			}
			if !instance.Enabled {
				return fmt.Errorf("rule %q uses disabled desync instance %q", r.ID, desync.Instance)
			}
			// "auto" is not a bundle: it is the rung deferring to what the KB has
			// learned, and the store never holds a file by that name. Looking it up
			// would reject exactly the rungs v1 left unpinned.
			if desync.Strategy != AutoStrategy && !contains(strategies[desync.Instance], desync.Strategy) {
				return fmt.Errorf("rule %q references unknown strategy %q on instance %q", r.ID, desync.Strategy, desync.Instance)
			}
		}
		if vpn := step.Action.VPN; vpn != nil {
			if _, ok := cfg.Pools[vpn.Pool]; !ok {
				return fmt.Errorf("rule %q references unknown pool %q", r.ID, vpn.Pool)
			}
		}
	}
	return nil
}

// ValidateDevices checks a device block: it must name sources, and its policy
// must be a reserved keyword or a pool that exists. A policy naming an undeclared
// pool is a load error rather than a device that quietly gets no override at
// all — which would read as "the PS5 is not going through VPN any more" with
// nothing in the log to say why.
func ValidateDevices(cfg *Configuration) error {
	for _, d := range cfg.Devices {
		if d.Name == "" {
			return fmt.Errorf("a device has no name")
		}
		if len(d.Sources) == 0 {
			return fmt.Errorf("device %q has no sources", d.Name)
		}
		if d.Policy == "" {
			return fmt.Errorf("device %q has no policy", d.Name)
		}
		if d.Policy == "direct" || d.Policy == "block" {
			continue
		}
		if _, ok := cfg.Pools[d.Policy]; !ok {
			return fmt.Errorf("device %q names unknown pool %q", d.Name, d.Policy)
		}
	}
	return nil
}

// ValidateReferencesAll reports the first broken reference across rules.
func ValidateReferencesAll(cfg *Configuration, rules []Rule, strategies map[string][]string) error {
	for _, rule := range rules {
		if err := rule.ValidateReferences(cfg, strategies); err != nil {
			return err
		}
	}
	return nil
}
