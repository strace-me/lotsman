package configv2

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/strace-me/lotsman/pkg/pools"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Configuration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*Configuration, error) {
	var cfg Configuration
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	return &cfg, nil
}

func (c *Configuration) ApplyDefaults() {
	for name, instance := range c.Desync.Instances {
		if instance.Table == "" {
			instance.Table = "inet nfqueue-" + name
			c.Desync.Instances[name] = instance
		}
	}
	if c.Runtime.StateDir == "" {
		c.Runtime.StateDir = DefaultStateDir
	}
	if c.Runtime.RuntimeDir == "" {
		c.Runtime.RuntimeDir = DefaultRuntimeDir
	}
	if c.Runtime.ProbeInterval == 0 {
		c.Runtime.ProbeInterval = Duration(10 * time.Second)
	}
	if c.Runtime.RefreshInterval == 0 {
		c.Runtime.RefreshInterval = Duration(5 * time.Minute)
	}
}

// reservedNames are the identifiers the runtime generates or owns. The VALUE is the
// reason, because a refusal the operator cannot act on is a refusal they will work
// around by guessing.
var reservedNames = map[string]string{
	"sel-probe": "it is the probe selector, aimed at one exit for a canary or a throughput " +
		"measurement. A service of the same name would have its traffic moved whenever a scan runs.",
	"sel-" + "-": "selectors are generated as sel-<service>, so a service named this way collides " +
		"with the naming scheme itself",
	"nfqueue-": "the per-instance nft table defaults to inet nfqueue-<instance>",
	"direct":   "it is the sing-box outbound every direct rung points at",
	"final":    "it is sing-box's own terminal route rule",
	"block":    "it is a reserved device policy word, not a pool",
}

// reservedPrefixes are names the runtime GENERATES, so the collision is with a
// pattern rather than a fixed word. Exact-match rejection would only catch a service
// literally called "sel-", which is not the collision anyone is going to hit.
var reservedPrefixes = []struct{ Prefix, Reason string }{
	{"sel-", "the routing selector is generated as sel-<service>, so this name would collide with " +
		"the generated tag of a DIFFERENT service and both would point at one exit"},
	{"rem-", "remediation routes are generated as rem-rq-<service> and rem-fb-<service>"},
	{"nfqueue-", "the per-instance nft table defaults to inet nfqueue-<instance>"},
	{"~", "a suffixed tag marks a detoured variant of a node; an operator name containing it " +
		"would collide with those"},
}

func reservedPrefixReason(name string) (string, bool) {
	for _, r := range reservedPrefixes {
		if strings.HasPrefix(name, r.Prefix) {
			return r.Reason, true
		}
	}
	return "", false
}

func poolNames(m map[string]Pool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func desyncInstanceNames(m map[string]Instance) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// describeMaterialised names the materialised forms a list used alongside srs_url,
// so the error says which field to delete rather than just "pick one".
func describeMaterialised(l List) string {
	var parts []string
	if l.Source != "" {
		parts = append(parts, "source")
	}
	if len(l.Files) > 0 {
		parts = append(parts, "files")
	}
	if len(l.URLs) > 0 {
		parts = append(parts, "urls")
	}
	if len(l.Domains) > 0 {
		parts = append(parts, "domains")
	}
	if len(l.CIDRs) > 0 {
		parts = append(parts, "cidrs")
	}
	return strings.Join(parts, "+")
}

// requiredMigrations must be present in a document's applied_migrations before it
// will load. The daemon sets it from the schema it expects; a document produced by
// an older build names fewer, and the load says exactly which one is missing.
var requiredMigrations []string

// SetRequiredMigrations declares the migration ids this build needs. It is a
// variable rather than a constant so a test can set it and put it back, and rather
// than a parameter so every existing call site keeps compiling — the alternative is
// threading a requirement list through four packages to express one string.
func SetRequiredMigrations(ids ...string) { requiredMigrations = ids }

// RequiredMigrations reports what this build needs, for the preflight to print.
func RequiredMigrations() []string { return requiredMigrations }

func (c *Configuration) Validate() error {
	applied := map[string]bool{}
	seen := map[string]bool{}
	for _, id := range c.AppliedMigrations {
		if seen[id] {
			return fmt.Errorf("applied_migrations lists %q twice", id)
		}
		seen[id] = true
		applied[id] = true
	}
	for _, id := range requiredMigrations {
		if !applied[id] {
			return fmt.Errorf("this build needs migration %q and the document does not record it "+
				"in applied_migrations: migrate before starting, or the config will load and behave "+
				"like an older schema (have %v)", id, c.AppliedMigrations)
		}
	}
	if c.Proxy.Engine != "" && !contains(ProxyEngines, c.Proxy.Engine) {
		return fmt.Errorf("unsupported proxy engine %q", c.Proxy.Engine)
	}
	if c.Proxy.Mode != "" && !contains(ProxyModes, c.Proxy.Mode) {
		return fmt.Errorf("unsupported proxy mode %q", c.Proxy.Mode)
	}
	if c.Runtime.Mode != "" && !contains(ProxyModes, c.Runtime.Mode) {
		return fmt.Errorf("unsupported runtime mode %q", c.Runtime.Mode)
	}
	// Names the runtime owns. An operator who takes one does not get a service or a
	// pool of their own — they get a collision with something internal, and the
	// failure arrives later, somewhere else, as a rule that routes nowhere. Each
	// entry carries the reason it is reserved, because an error that says only
	// "reserved" makes the reader guess which of the four hundred names in the
	// generated config they are allowed to touch.
	for _, name := range desyncInstanceNames(c.Desync.Instances) {
		if reason, taken := reservedNames[name]; taken {
			return fmt.Errorf("desync instance %q is a RESERVED name: %s. Pick another — the "+
				"runtime generates tags and tables from this name, so a collision renames something "+
				"you did not write", name, reason)
		}
		if reason, taken := reservedPrefixReason(name); taken {
			return fmt.Errorf("desync instance %q starts with a RESERVED prefix: %s", name, reason)
		}
	}
	for _, name := range poolNames(c.Pools) {
		if reason, taken := reservedNames[name]; taken {
			return fmt.Errorf("pool %q is a RESERVED name: %s", name, reason)
		}
		if reason, taken := reservedPrefixReason(name); taken {
			return fmt.Errorf("pool %q starts with a RESERVED prefix: %s", name, reason)
		}
	}

	tableOwner := map[string]string{}
	for name, instance := range c.Desync.Instances {
		if !contains(DesyncEngines, instance.Engine) {
			return fmt.Errorf("desync instance %q has unknown engine %q", name, instance.Engine)
		}
		if instance.Table != "" && !nftTablePattern.MatchString(instance.Table) {
			return fmt.Errorf("desync instance %q has invalid nft table %q", name, instance.Table)
		}
		// Compare the EFFECTIVE table: the per-instance default is applied while the
		// document loads, so a struct handed straight to Validate still carries "" and
		// two such instances would otherwise both hash to the empty key and be
		// reported as colliding when their real tables are distinct.
		table := instance.Table
		if table == "" {
			table = "inet nfqueue-" + name
		}
		if prev, taken := tableOwner[table]; taken {
			return fmt.Errorf("desync instances %q and %q share nft table %q: an instance installs by "+
				"DELETING and replacing its table, so one would wipe the other's rules — and the "+
				"victim instance would keep answering as if it were installed", prev, name, table)
		}
		tableOwner[table] = name
	}
	for name, p := range c.Pools {
		if p.MinCoverage < 0 || p.MinCoverage > 1 {
			return fmt.Errorf("pool %q has min_coverage %v, want a fraction in [0,1]", name, p.MinCoverage)
		}
		if p.Limit < 0 {
			return fmt.Errorf("pool %q has a negative limit", name)
		}
		// limit and min_coverage are a RANKING's knobs. On a url_test pool they
		// would be read as "the cap was applied" while sing-box keeps choosing by
		// latency — a setting that looks live and does nothing, which is the
		// failure this project has an axiom about. Say it at load instead.
		if p.Type != pools.TypeSpeedtestTop {
			if p.Limit != 0 {
				return fmt.Errorf("pool %q is type %q and takes no limit: sing-box chooses by latency inside any url_test group, so the cap would be inert", name, p.Type)
			}
			if p.MinCoverage != 0 {
				return fmt.Errorf("pool %q is type %q, which is never ranked, so min_coverage is inert", name, p.Type)
			}
		}
		for _, cc := range append(append([]string{}, p.CountriesInclude...), p.CountriesExclude...) {
			if !iso2.MatchString(cc) {
				return fmt.Errorf("pool %q names country %q, want ISO-3166 alpha-2 (e.g. de)", name, cc)
			}
		}
	}
	if err := ValidateDevices(c); err != nil {
		return err
	}
	for name, source := range c.DataSources {
		if !contains(DataSourceTypes, source.Type) {
			return fmt.Errorf("data source %q has unknown type %q (have %v)", name, source.Type, DataSourceTypes)
		}
		if source.Refresh < 0 {
			return fmt.Errorf("data source %q has a negative refresh interval", name)
		}
	}
	for _, group := range []struct {
		kind  string
		lists map[string]List
	}{
		{"domains", c.Lists.Domains},
		{"ips", c.Lists.IPs},
	} {
		for name, list := range group.lists {
			// A list is EITHER handed to sing-box as a remote rule-set or resolved by
			// us — never both. Allowing both would produce a config with two
			// definitions of the same membership and nothing to say which one routes.
			if list.SRSURL != "" {
				if list.Source != "" || len(list.Files) > 0 || len(list.URLs) > 0 ||
					len(list.Domains) > 0 || len(list.CIDRs) > 0 {
					return fmt.Errorf("%s list %q declares srs_url AND %s: pick one — "+
						"a remote rule-set is sing-box's to fetch, a local one is ours to resolve, "+
						"and both together means two definitions of the same membership",
						group.kind, name, describeMaterialised(list))
				}
				if !strings.HasPrefix(list.SRSURL, "https://") {
					return fmt.Errorf("%s list %q has srs_url %q, want an https URL: sing-box "+
						"fetches this itself with no integrity check we could add", group.kind, name, list.SRSURL)
				}
				continue
			}
			if list.Source == "" && len(list.Files) == 0 && len(list.URLs) == 0 && len(list.Domains) == 0 && len(list.CIDRs) == 0 {
				return fmt.Errorf("%s list %q has no sources", group.kind, name)
			}
			if list.Source != "" {
				if _, ok := c.DataSources[list.Source]; !ok {
					return fmt.Errorf("%s list %q references unknown data source %q", group.kind, name, list.Source)
				}
				if list.Dataset == "" || list.Category == "" {
					return fmt.Errorf("%s list %q needs both dataset and category", group.kind, name)
				}
			}
		}
	}
	for name, pool := range c.Pools {
		if pool.Type == "" {
			return fmt.Errorf("pool %q has no type", name)
		}
	}
	return nil
}
