package configv2

import (
	"fmt"
)

// builtinPools are always in scope, because FromV2 overlays the document on top of
// pools.Builtin() rather than replacing it. Listing them here is the validator's
// half of that promise; changing one set without the other is how a rule ends up
// valid at run time and rejected at load.
var builtinPools = map[string]bool{
	"vpn_url_test": true, "vpn_url_test_udp": true, "emergency_pool": true,
}

// ValidateReferences checks a rule against the loaded configuration: every
// named list, pool, desync instance and strategy it references must exist.
// strategies maps a desync instance name to the strategy ids its store holds.
func (r *Rule) ValidateReferences(cfg *Configuration, strategies map[string][]string) error {
	if len(r.Match.Domains) == 0 && len(r.Match.DomainLists) == 0 && len(r.Match.IPLists) == 0 && len(r.Match.CIDRs) == 0 {
		return fmt.Errorf("rule %q has an empty match", r.ID)
	}
	for _, name := range r.Match.DomainLists {
		// A domain list may be EITHER a `lists.domains` entry, which becomes a
		// sing-box rule-set, or a `hostlists` entry, whose domains are merged into the
		// rule for the desync engine to read. Both are in scope under the same key,
		// because a rule routinely needs both — see buildService — and the document
		// says which one a name means. A name that is neither is refused rather than
		// resolved to something plausible.
		_, isList := cfg.Lists.Domains[name]
		_, isPack := cfg.hostlistNames()[name]
		if !isList && !isPack {
			return fmt.Errorf("rule %q references unknown domain list %q: it is neither a lists.domains entry nor a hostlists entry", r.ID, name)
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
			// An OVERRIDE means one bundle serves every rule, so a per-rule strategy
			// pin is not merely unresolvable — it is IGNORED. Validating it anyway
			// refused a document whose pin could never apply: the migrated config of a
			// preset deployment names a bundle that only exists as a preset, and the
			// loader demanded it from the store it will never consult. A document has to
			// load if the thing it names cannot affect what runs; the pin being dead is
			// reported as a gap, not enforced as an error.
			//
			// "auto" is not a bundle either: it is the rung deferring to what the KB has
			// learned, and the store never holds a file by that name. Looking it up
			// would reject exactly the rungs v1 left unpinned.
			if cfg.Desync.Override == nil && desync.Strategy != AutoStrategy &&
				!contains(strategies[desync.Instance], desync.Strategy) {
				return fmt.Errorf("rule %q references unknown strategy %q on instance %q", r.ID, desync.Strategy, desync.Instance)
			}
		}
		if vpn := step.Action.VPN; vpn != nil {
			// The BUILTIN pools count as declared even though the document does not
			// list them: the runtime projection overlays the document ON TOP of
			// pools.Builtin(), so a rule naming vpn_url_test or emergency_pool
			// resolves fine at run time. Checking only the document made a working
			// rule fail to load, and naming a built-in pool is exactly what a
			// PARTIAL v2 file should be able to say.
			if err := requirePool(cfg, r.ID, "pool", vpn.Pool); err != nil {
				return err
			}
			if vpn.Detour == "" {
				continue
			}
			if err := requirePool(cfg, r.ID, "detour", vpn.Detour); err != nil {
				return err
			}
			// The degenerate ring. Checked here as well as in ValidateDetours because
			// this is a mistake you can make in a single rule, with no second rule
			// needed to close it.
			if vpn.Detour == vpn.Pool {
				return fmt.Errorf("rule %q detours pool %q through itself", r.ID, vpn.Pool)
			}
		}
	}
	return nil
}

// requirePool accepts a declared pool or a built-in one, naming which of the two
// roles failed so the error says "detour names an unknown pool" rather than making
// the reader work out which of the two pools a rung carries.
func requirePool(cfg *Configuration, ruleID, role, name string) error {
	if _, ok := cfg.Pools[name]; ok || builtinPools[name] {
		return nil
	}
	return fmt.Errorf("rule %q %s names unknown pool %q", ruleID, role, name)
}

// ResolveListTag turns a v2 list NAME into the sing-box rule-set tag. It is the one
// exported answer to that question, and both the generator's path lookup and the
// projection's remote-rule-set map go through it: a tag is a PATH (by prefix) and a
// URL (when the list is remote), so a second mapping would be a second answer to
// "which rule-set is this list".
//
// kind is "geosite" or "geoip", matching the namespace the name came from.
func (c *Configuration) ResolveListTag(name, kind string) (string, error) {
	return resolveList(c, name, kind)
}

// ValidateDetours refuses a detour cycle across the whole document.
//
// Each rule only ever names one detour, so the graph is a set of arcs pool→pool and
// a cycle needs at least two rules pointing at each other. Walking it with a `seen`
// set is the check; the alternative — comparing every pair of rules — is quadratic
// and would miss a three-hop ring.
func ValidateDetours(rules []Rule) error {
	arc := map[string]string{} // pool -> the pool it detours through
	for _, r := range rules {
		for _, step := range r.Chain {
			vpn := step.Action.VPN
			if vpn == nil || vpn.Detour == "" {
				continue
			}
			if prev, ok := arc[vpn.Pool]; ok && prev != vpn.Detour {
				return fmt.Errorf("rule %q sends pool %q through %q, but another rule sends it through %q: "+
					"one pool has one path out; split the pools or drop a detour",
					r.ID, vpn.Pool, vpn.Detour, prev)
			}
			arc[vpn.Pool] = vpn.Detour
		}
	}
	// pool -> seen set, so a ring is named with the pools that close it.
	seenFrom := map[string]map[string]bool{}
	for start := range arc {
		walk := []string{start}
		inWalk := map[string]bool{}
		for len(walk) > 0 {
			cur := walk[len(walk)-1]
			walk = walk[:len(walk)-1]
			if inWalk[cur] {
				return fmt.Errorf("detour cycle: %v -> %s", seenFrom[start], cur)
			}
			inWalk[cur] = true
			if seenFrom[start] == nil {
				seenFrom[start] = map[string]bool{}
			}
			seenFrom[start][cur] = true
			if next, ok := arc[cur]; ok {
				walk = append(walk, next)
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
