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
	if len(r.Chain) == 0 {
		return fmt.Errorf("rule %q has an empty chain", r.ID)
	}
	seenTier := map[string]bool{}
	for _, step := range r.Chain {
		if seenTier[step.Tier] {
			return fmt.Errorf("rule %q repeats chain tier %q", r.ID, step.Tier)
		}
		seenTier[step.Tier] = true
		if err := step.Action.validate(r.ID); err != nil {
			return err
		}
	}
	return nil
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

// LoadRules reads every *.yaml rule in dir, ordered by priority (descending).
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
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority > rules[j].Priority })
	return rules, nil
}
