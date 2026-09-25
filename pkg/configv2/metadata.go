package configv2

import (
	"sort"
)

// StrategyInfo is what a UI needs to render a desync step: the bundle is
// addressed as {instance, strategy}, and the engine decides which strategies
// are valid for it.
type StrategyInfo struct {
	Instance string   `json:"instance"`
	Engine   string   `json:"engine"`
	Strategy string   `json:"strategy"`
	Path     string   `json:"path"`
	Revision string   `json:"revision"`
	Engines  []string `json:"engines"`
}

// Metadata is the single document the GUI and LuCI read for autocomplete and
// validation hints. It is derived from the same enum tables the loader
// validates against, so the two cannot drift.
type Metadata struct {
	Paths         map[string]string   `json:"paths"`
	ProxyEngines  []string            `json:"proxy_engines"`
	ProxyModes    []string            `json:"proxy_modes"`
	DesyncEngines []string            `json:"desync_engines"`
	ProbeTypes    []string            `json:"probe_types"`
	ChainTiers    []string            `json:"chain_tiers"`
	ChainActions  []ChainActionMeta   `json:"chain_actions"`
	Providers     []string            `json:"providers"`
	DomainLists   []string            `json:"domain_lists"`
	IPLists       []string            `json:"ip_lists"`
	Pools         []string            `json:"pools"`
	Desync        map[string]Instance `json:"desync_instances"`
	Strategies    []StrategyInfo      `json:"strategies"`
}

func BuildMetadata(cfg *Configuration, rules []Rule, pools []string, strategies []StrategyInfo) Metadata {
	if cfg == nil {
		cfg = &Configuration{}
	}
	stateDir, runtimeDir := cfg.Runtime.StateDir, cfg.Runtime.RuntimeDir
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	if runtimeDir == "" {
		runtimeDir = DefaultRuntimeDir
	}
	if strategies == nil {
		strategies = []StrategyInfo{}
	} else {
		sort.SliceStable(strategies, func(i, j int) bool {
			if strategies[i].Instance != strategies[j].Instance {
				return strategies[i].Instance < strategies[j].Instance
			}
			return strategies[i].Strategy < strategies[j].Strategy
		})
	}
	desync := cfg.Desync.Instances
	if desync == nil {
		desync = map[string]Instance{}
	}
	return Metadata{
		Paths: map[string]string{
			"configuration": DefaultPath,
			"rules":         RulesDir,
			"state_dir":     stateDir,
			"runtime_dir":   runtimeDir,
		},
		ProxyEngines:  ProxyEngines,
		ProxyModes:    ProxyModes,
		DesyncEngines: DesyncEngines,
		ProbeTypes:    ProbeTypes,
		ChainTiers:    ChainTiers,
		ChainActions:  ChainActions,
		Providers:     sortedKeys(cfg.DataSources),
		DomainLists:   sortedKeys(cfg.Lists.Domains),
		IPLists:       sortedKeys(cfg.Lists.IPs),
		Pools:         sorted(pools),
		Desync:        desync,
		Strategies:    strategies,
	}
}

func sortedKeys[T any](in map[string]T) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return sorted(out)
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
