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
			if !contains(strategies[desync.Instance], desync.Strategy) {
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

// ValidateReferencesAll reports the first broken reference across rules.
func ValidateReferencesAll(cfg *Configuration, rules []Rule, strategies map[string][]string) error {
	for _, rule := range rules {
		if err := rule.ValidateReferences(cfg, strategies); err != nil {
			return err
		}
	}
	return nil
}
