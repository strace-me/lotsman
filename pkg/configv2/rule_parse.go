package configv2

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func ParseRule(data []byte) (*Rule, error) {
	var rule Rule
	if err := yaml.Unmarshal(data, &rule); err != nil {
		return nil, err
	}
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	return &rule, nil
}

func (r *Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("rule has no id")
	}
	if r.Probe != nil && !contains(ProbeTypes, r.Probe.Type) {
		return fmt.Errorf("rule %q has unknown probe type %q", r.ID, r.Probe.Type)
	}
	if err := r.Behavior.Validate(r.ID); err != nil {
		return err
	}
	if len(r.Chain) == 0 {
		return fmt.Errorf("rule %q has an empty chain", r.ID)
	}
	// Repeating a tier is fine and expected. Going BACKWARDS is not: the tiers
	// name how safe a rung is, so `emergency` followed by `preferred` reads as
	// "climb back to the best path after the last resort", which is the opposite
	// of what the ladder does.
	previousRank, previousTier := 0, ""
	for _, step := range r.Chain {
		if err := step.Action.validate(r.ID); err != nil {
			return err
		}
		if step.Tier == "" {
			continue // unlabelled: order alone carries it
		}
		rank := tierRank(step.Tier)
		if rank < previousRank {
			return fmt.Errorf("rule %q steps back from tier %q to %q; tiers may repeat but not go backwards",
				r.ID, previousTier, step.Tier)
		}
		previousRank, previousTier = rank, step.Tier
	}
	return nil
}

func tierRank(tier string) int {
	return indexOf(ChainTiers, tier)
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func (a Action) validate(ruleID string) error {
	actions := 0
	if a.Desync != nil {
		actions++
		if a.Desync.Instance == "" || a.Desync.Strategy == "" {
			return fmt.Errorf("rule %q has an incomplete desync action", ruleID)
		}
	}
	if a.VPN != nil {
		actions++
		if a.VPN.Pool == "" {
			return fmt.Errorf("rule %q has a vpn action without a pool", ruleID)
		}
	}
	if a.Direct {
		actions++
	}
	if a.Bypass {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("rule %q has a chain step with %d actions, want exactly 1", ruleID, actions)
	}
	return nil
}

// LoadRules reads every *.yaml rule in dir in EFFECTIVE order: priority first,
// then name. It is the same order v1 emits routes in (pkg/singbox.Generate
// comments it as "first-match-wins, so emit by Priority (lower first), then name
// for a stable tie-break"), and matching it is what keeps a migrated rule landing
// where the live config put it. An earlier version of this loader sorted priority
// DESCENDING, which silently reversed every rule's precedence.
func LoadRules(dir string) ([]Rule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var rules []Rule
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		rule, err := ParseRule(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if want := strings.TrimSuffix(entry.Name(), ".yaml"); rule.ID != want {
			return nil, fmt.Errorf("%s: rule id %q does not match its file name", entry.Name(), rule.ID)
		}
		rules = append(rules, *rule)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
	return rules, nil
}
