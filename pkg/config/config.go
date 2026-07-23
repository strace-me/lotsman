// Package config loads the declarative YAML into domain types: the service
// registry, subscription declarations, and the pool set. It is the
// composition root for static configuration — it imports the domain packages
// rather than the other way around, and assigns derived fields (chain
// positions) and validates before handing clean structs to the daemon.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// Config is the parsed, validated configuration.
type Config struct {
	Registry        *registry.Registry
	Subscriptions   []subscription.Declaration
	Pools           *pools.Set
	Zapret          []zapret.Instance
	Devices         []registry.Device
	Hostlists       []Hostlist
	Strategies      []strategy.Definition // declared strategies, added on top of the builtin catalog
	UTLSFingerprint string                // default tls.utls fingerprint injected into TCP TLS outbounds lacking one ("" = off)
	SingboxVersion  string                // target sing-box version (e.g. "1.12.17"); gates version-specific knobs ("" = generator baseline)
	FakeIP          *FakeIP               // fakeip DNS section (nil = off)
	Multiplex       *Multiplex            // default outbound multiplex for TCP proxies (nil = off)
	SubViaPool      string                // route subscription endpoint hosts through this VPN pool (LOT-28); needs -probe-proxy ("" = off, direct fetch)
}

// FakeIP enables the generated fakeip DNS section (domain-accurate routing via
// synthetic IPs). Ranges default to the standard reserved blocks when empty.
type FakeIP struct {
	Inet4Range string
	Inet6Range string
	Resolver   string // real upstream for non-fake queries (https URL or host[:port] udp)
}

// Multiplex enables a default `multiplex` block on TCP proxy outbounds
// (vless/vmess/trojan/shadowsocks). Server-cooperative, so off by default.
type Multiplex struct {
	Protocol       string // smux (default) | yamux | h2mux
	MaxConnections int
	MinStreams     int
	Padding        bool
	BrutalUp       int // brutal up_mbps (0 = no brutal)
	BrutalDown     int // brutal down_mbps
}

// Hostlist declares a merged domain list to (re)build periodically: fetch every
// source, drop everything on an exclude source, dedup, and write to Out (the
// hostlist file nfqws reads).
type Hostlist struct {
	Name         string
	Out          string
	Sources      []aggregate.Source
	Exclude      []aggregate.Source
	MinKeepRatio float64 // global shrink guard: reject a rebuild below this fraction of the last good count (0 = disabled)
}

// --- on-disk shape ---

type fileYAML struct {
	Services        []serviceYAML              `yaml:"services"`
	Categories      map[string]categoryYAML    `yaml:"categories"`
	Subscriptions   []subscription.Declaration `yaml:"subscriptions"`
	Pools           map[string]poolYAML        `yaml:"pools"`
	Zapret          zapretYAML                 `yaml:"zapret"`
	Devices         []deviceYAML               `yaml:"devices"`
	Hostlists       []hostlistYAML             `yaml:"hostlists"`
	Strategies      []strategyYAML             `yaml:"strategies"`
	UTLSFingerprint string                     `yaml:"utls_fingerprint"`
	SingboxVersion  string                     `yaml:"singbox_version"`
	FakeIP          *fakeipYAML                `yaml:"fakeip"`
	Multiplex       *multiplexYAML             `yaml:"multiplex"`
	SubViaPool      string                     `yaml:"subscription_via_pool"`
}

type fakeipYAML struct {
	Enabled    bool   `yaml:"enabled"`
	Inet4Range string `yaml:"inet4_range"`
	Inet6Range string `yaml:"inet6_range"`
	Resolver   string `yaml:"resolver"`
}

type multiplexYAML struct {
	Enabled        bool   `yaml:"enabled"`
	Protocol       string `yaml:"protocol"`
	MaxConnections int    `yaml:"max_connections"`
	MinStreams     int    `yaml:"min_streams"`
	Padding        bool   `yaml:"padding"`
	BrutalUp       int    `yaml:"brutal_up_mbps"`
	BrutalDown     int    `yaml:"brutal_down_mbps"`
}

type categoryYAML struct {
	RequiredCaps []string        `yaml:"required_caps"`
	DefaultChain []chainStepYAML `yaml:"default_chain"`
	Profile      string          `yaml:"profile"` // group default profile (services inherit if unset) — LOT-23
	Sticky       bool            `yaml:"sticky"`  // group default sticky (services inherit if unset) — LOT-23
}

type strategyYAML struct {
	ID         string   `yaml:"id"`
	Class      string   `yaml:"class"`
	NFQWSArgs  []string `yaml:"nfqws_args"`
	BlockTypes []string `yaml:"block_types"`
	Notes      string   `yaml:"notes"`
}

type hostlistYAML struct {
	Name         string   `yaml:"name"`
	Out          string   `yaml:"out"`
	Sources      []string `yaml:"sources"`
	Exclude      []string `yaml:"exclude"`
	MinKeepRatio float64  `yaml:"min_keep_ratio"`
}

type deviceYAML struct {
	Name    string   `yaml:"name"`
	Sources []string `yaml:"sources"`
	Policy  string   `yaml:"policy"`
}

type zapretYAML struct {
	Instances []instanceYAML `yaml:"instances"`
}

type instanceYAML struct {
	Name      string      `yaml:"name"`
	QNum      int         `yaml:"qnum"`
	Capture   captureYAML `yaml:"capture"`
	Connbytes int         `yaml:"connbytes"`
}

type captureYAML struct {
	TCP []any `yaml:"tcp"` // ints or "range" strings
	UDP []any `yaml:"udp"`
}

type serviceYAML struct {
	Name           string          `yaml:"name"`
	Category       string          `yaml:"category"`
	ProbeType      string          `yaml:"probe_type"`
	ProbeTarget    string          `yaml:"probe_target"`
	RuleSets       []string        `yaml:"rule_sets"`
	Domains        []string        `yaml:"domains"`
	ExcludeDomains []string        `yaml:"exclude_domains"` // raw-pass through nfqws desync (composer --hostlist-exclude; LOT-36 CDNs)
	SpreadClients  []string        `yaml:"spread_clients"`  // LAN client CIDRs spread across this service's VPN nodes (LOT-23)
	IPs            []string        `yaml:"ips"`
	IPsFile        string          `yaml:"ips_file"` // optional file of extra CIDRs (one per line, # comments); merged into IPs. Home for runtime-learned sets (e.g. Discord voice).
	Sticky         *bool           `yaml:"sticky"`   // pointer so "unset" (inherit category) differs from explicit false — LOT-23
	Profile        string          `yaml:"profile"`
	Static         bool            `yaml:"static"`
	EscalateAfter  int             `yaml:"escalate_after"`
	RecoverAfter   int             `yaml:"recover_after"`
	Priority       int             `yaml:"priority"`
	TLSFragment    bool            `yaml:"tls_fragment"`
	Chain          []chainStepYAML `yaml:"chain"`
}

type chainStepYAML struct {
	State       string `yaml:"state"`
	Class       string `yaml:"class"`
	StrategyID  string `yaml:"strategy_id"`
	ProbeType   string `yaml:"probe_type"`   // per-rung probe override (LOT-3): http|tcp|stun|quic (empty = inherit service)
	ProbeTarget string `yaml:"probe_target"` // per-rung probe target override
}

type poolYAML struct {
	Type        string     `yaml:"type"`
	Filter      filterYAML `yaml:"filter"`
	Warmup      bool       `yaml:"warmup"`       // keep this pool always-hot (idle_timeout 0s) for instant failover
	Interval    string     `yaml:"interval"`     // url-test probe period, e.g. "1m" (empty = default)
	IdleTimeout string     `yaml:"idle_timeout"` // stop probing after idle, e.g. "30m" or "0s" (empty = sing-box default)
}

type filterYAML struct {
	Caps             []string `yaml:"caps"`
	TagsInclude      []string `yaml:"tags_include"`
	TagsExclude      []string `yaml:"tags_exclude"`
	CountriesInclude []string `yaml:"countries_include"`
	CountriesExclude []string `yaml:"countries_exclude"`
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses and validates config bytes.
func Parse(data []byte) (*Config, error) {
	var f fileYAML
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("config: yaml: %w", err)
	}

	cats, err := buildCategories(f.Categories)
	if err != nil {
		return nil, err
	}
	reg, err := buildRegistry(f.Services, cats)
	if err != nil {
		return nil, err
	}
	pl, err := buildPools(f.Pools)
	if err != nil {
		return nil, err
	}
	instances, err := buildZapret(f.Zapret)
	if err != nil {
		return nil, err
	}
	devices := make([]registry.Device, 0, len(f.Devices))
	for _, d := range f.Devices {
		if d.Name == "" {
			return nil, fmt.Errorf("config: device with empty name")
		}
		devices = append(devices, registry.Device{Name: d.Name, Sources: d.Sources, Policy: d.Policy})
	}
	hostlists, err := buildHostlists(f.Hostlists)
	if err != nil {
		return nil, err
	}
	// Fill in the standard pools a chain references but the config did not declare,
	// so a config that leans on the builtin categories does not have to hand-copy
	// the same three pool blocks into every file.
	injectBuiltinPools(pl, reg, f.SubViaPool, devices)
	strategies, err := buildStrategies(f.Strategies)
	if err != nil {
		return nil, err
	}
	var fakeip *FakeIP
	if f.FakeIP != nil && f.FakeIP.Enabled {
		fakeip = &FakeIP{Inet4Range: f.FakeIP.Inet4Range, Inet6Range: f.FakeIP.Inet6Range, Resolver: f.FakeIP.Resolver}
	}
	var mux *Multiplex
	if f.Multiplex != nil && f.Multiplex.Enabled {
		mux = &Multiplex{Protocol: f.Multiplex.Protocol, MaxConnections: f.Multiplex.MaxConnections, MinStreams: f.Multiplex.MinStreams, Padding: f.Multiplex.Padding, BrutalUp: f.Multiplex.BrutalUp, BrutalDown: f.Multiplex.BrutalDown}
	}
	return &Config{Registry: reg, Subscriptions: f.Subscriptions, Pools: pl, Zapret: instances, Devices: devices, Hostlists: hostlists, Strategies: strategies, UTLSFingerprint: f.UTLSFingerprint, SingboxVersion: f.SingboxVersion, FakeIP: fakeip, Multiplex: mux, SubViaPool: f.SubViaPool}, nil
}

// buildCategories starts from the builtin seven and applies config overrides
// (a config category with the same name replaces the builtin; a new name adds).
func buildCategories(in map[string]categoryYAML) (map[string]registry.Category, error) {
	cats := registry.BuiltinCategories()
	for name, c := range in {
		if name == "" {
			return nil, fmt.Errorf("config: category with empty name")
		}
		chain := make([]registry.ChainStep, 0, len(c.DefaultChain))
		for i, step := range c.DefaultChain {
			if !validState(step.State) {
				return nil, fmt.Errorf("config: category %q step %d: unknown state %q", name, i, step.State)
			}
			if !validClass(step.Class) {
				return nil, fmt.Errorf("config: category %q step %d: unknown class %q", name, i, step.Class)
			}
			chain = append(chain, registry.ChainStep{State: step.State, StrategyClass: step.Class, StrategyID: step.StrategyID, ProbeType: step.ProbeType, ProbeTarget: step.ProbeTarget})
		}
		cats[name] = registry.Category{Name: name, RequiredCaps: c.RequiredCaps, DefaultChain: chain,
			DefaultProfile: c.Profile, DefaultSticky: c.Sticky}
	}
	return cats, nil
}

func buildStrategies(in []strategyYAML) ([]strategy.Definition, error) {
	out := make([]strategy.Definition, 0, len(in))
	for _, s := range in {
		if s.ID == "" {
			return nil, fmt.Errorf("config: strategy with empty id")
		}
		if !validClass(s.Class) {
			return nil, fmt.Errorf("config: strategy %q has unknown class %q", s.ID, s.Class)
		}
		out = append(out, strategy.Definition{
			ID:         s.ID,
			Class:      s.Class,
			NFQWSArgs:  s.NFQWSArgs,
			BlockTypes: s.BlockTypes,
			Notes:      s.Notes,
		})
	}
	return out, nil
}

func buildHostlists(in []hostlistYAML) ([]Hostlist, error) {
	out := make([]Hostlist, 0, len(in))
	for _, h := range in {
		if h.Name == "" {
			return nil, fmt.Errorf("config: hostlist with empty name")
		}
		if h.Out == "" {
			return nil, fmt.Errorf("config: hostlist %q has no out path", h.Name)
		}
		if len(h.Sources) == 0 {
			return nil, fmt.Errorf("config: hostlist %q has no sources", h.Name)
		}
		if h.MinKeepRatio < 0 || h.MinKeepRatio > 1 {
			return nil, fmt.Errorf("config: hostlist %q: min_keep_ratio %v out of [0,1]", h.Name, h.MinKeepRatio)
		}
		out = append(out, Hostlist{
			Name:         h.Name,
			Out:          h.Out,
			Sources:      toSources(h.Sources),
			Exclude:      toSources(h.Exclude),
			MinKeepRatio: h.MinKeepRatio,
		})
	}
	return out, nil
}

// toSources names each source by its URL for traceability in merge errors.
func toSources(urls []string) []aggregate.Source {
	s := make([]aggregate.Source, 0, len(urls))
	for _, u := range urls {
		s = append(s, aggregate.Source{Name: u, URL: u})
	}
	return s
}

func buildZapret(z zapretYAML) ([]zapret.Instance, error) {
	out := make([]zapret.Instance, 0, len(z.Instances))
	for _, in := range z.Instances {
		out = append(out, zapret.Instance{
			Name:      in.Name,
			QNum:      in.QNum,
			Capture:   zapret.Capture{TCP: portList(in.Capture.TCP), UDP: portList(in.Capture.UDP)},
			Connbytes: in.Connbytes,
		})
	}
	if err := zapret.Validate(out); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return out, nil
}

// portList stringifies a YAML port list whose items may be ints (443) or range
// strings ("50000-50100").
func portList(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch v := it.(type) {
		case int:
			out = append(out, strconv.Itoa(v))
		case string:
			out = append(out, v)
		}
	}
	return out
}

func buildRegistry(svcs []serviceYAML, cats map[string]registry.Category) (*registry.Registry, error) {
	if len(svcs) == 0 {
		return nil, fmt.Errorf("config: no services defined")
	}
	reg := &registry.Registry{Services: make(map[string]registry.Service, len(svcs))}
	for _, s := range svcs {
		if s.Name == "" {
			return nil, fmt.Errorf("config: service with empty name")
		}
		if _, dup := reg.Services[s.Name]; dup {
			return nil, fmt.Errorf("config: duplicate service %q", s.Name)
		}
		if !validProbeType(s.ProbeType) {
			return nil, fmt.Errorf("config: service %q: unknown probe_type %q (want http/tcp/stun)", s.Name, s.ProbeType)
		}
		if s.ProbeType == dataplane.ProbeQUIC {
			return nil, fmt.Errorf("config: service %q: probe_type quic is per-rung only — set it on a direct/zapret chain step, not service-level (it false-fails VPN rungs)", s.Name)
		}
		chain, err := resolveChain(s, cats)
		if err != nil {
			return nil, err
		}
		ips, err := normalizeIPs(s.Name, s.IPs)
		if err != nil {
			return nil, err
		}
		if s.IPsFile != "" {
			fileIPs, err := loadIPsFile(s.Name, s.IPsFile)
			if err != nil {
				return nil, err
			}
			ips = mergeCIDRs(ips, fileIPs)
		}
		for _, d := range s.Domains {
			if !aggregate.ValidDomain(d) {
				return nil, fmt.Errorf("config: service %q: invalid domain %q", s.Name, d)
			}
		}
		// Group-level lever (LOT-23): inherit profile/sticky from the category when the
		// service does not set its own (profile unset = "", sticky unset = nil pointer).
		cat := cats[s.Category]
		profile := s.Profile
		if profile == "" {
			profile = cat.DefaultProfile
		}
		sticky := cat.DefaultSticky
		if s.Sticky != nil {
			sticky = *s.Sticky
		}
		if !validProfile(profile) {
			return nil, fmt.Errorf("config: service %q: unknown profile %q (want general/voice/streaming/gaming)", s.Name, profile)
		}
		reg.Services[s.Name] = registry.Service{
			Name:           s.Name,
			Category:       s.Category,
			ProbeType:      s.ProbeType,
			ProbeTarget:    s.ProbeTarget,
			RuleSets:       s.RuleSets,
			Domains:        s.Domains,
			ExcludeDomains: s.ExcludeDomains,
			SpreadClients:  s.SpreadClients,
			IPs:            ips,
			Sticky:         sticky,
			Profile:        profile,
			Static:         s.Static,
			EscalateAfter:  s.EscalateAfter,
			RecoverAfter:   s.RecoverAfter,
			Priority:       s.Priority,
			TLSFragment:    s.TLSFragment,
			Chain:          chain,
		}
	}
	return reg, nil
}

// normalizeIPs validates each inline IP/CIDR and returns canonical CIDR form
// (bare IP → /32 or /128), rejecting junk at config time.
func normalizeIPs(service string, ips []string) ([]string, error) {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		c, ok := aggregate.NormalizeCIDR(ip)
		if !ok {
			return nil, fmt.Errorf("config: service %q: invalid ip/cidr %q", service, ip)
		}
		out = append(out, c)
	}
	return out, nil
}

// loadIPsFile reads extra CIDRs from a file (one per line; blank lines and
// "#" comments ignored), normalizing each. A missing file is not an error — it
// is the expected initial state before runtime learning populates it — but a
// malformed CIDR is, so typos are caught. Used for the Discord voice set.
func loadIPsFile(service, path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: service %q: ips_file %q: %w", service, path, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c, ok := aggregate.NormalizeCIDR(line)
		if !ok {
			return nil, fmt.Errorf("config: service %q: ips_file %q: invalid ip/cidr %q", service, path, line)
		}
		out = append(out, c)
	}
	return out, nil
}

// mergeCIDRs appends b to a, skipping CIDRs already present in a. Order-stable.
func mergeCIDRs(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			a = append(a, c)
			seen[c] = true
		}
	}
	return a
}

// resolveChain returns a service's chain: its explicit steps if any, otherwise
// the DefaultChain inherited from its category. Positions are assigned here so
// both paths produce a consistent denormalized chain.
func resolveChain(s serviceYAML, cats map[string]registry.Category) ([]registry.ChainStep, error) {
	var steps []registry.ChainStep
	if len(s.Chain) > 0 {
		for _, step := range s.Chain {
			steps = append(steps, registry.ChainStep{State: step.State, StrategyClass: step.Class, StrategyID: step.StrategyID, ProbeType: step.ProbeType, ProbeTarget: step.ProbeTarget})
		}
	} else {
		cat, ok := cats[s.Category]
		if !ok {
			return nil, fmt.Errorf("config: service %q has no chain and unknown category %q", s.Name, s.Category)
		}
		if len(cat.DefaultChain) == 0 {
			return nil, fmt.Errorf("config: service %q has no chain and category %q has no default_chain", s.Name, s.Category)
		}
		steps = append(steps, cat.DefaultChain...)
	}
	out := make([]registry.ChainStep, 0, len(steps))
	for i, step := range steps {
		if !validState(step.State) {
			return nil, fmt.Errorf("config: service %q step %d: unknown state %q", s.Name, i, step.State)
		}
		if !validClass(step.StrategyClass) {
			return nil, fmt.Errorf("config: service %q step %d: unknown class %q", s.Name, i, step.StrategyClass)
		}
		if !validProbeType(step.ProbeType) {
			return nil, fmt.Errorf("config: service %q step %d: unknown probe_type %q (want http/tcp/stun/quic)", s.Name, i, step.ProbeType)
		}
		// A box-direct QUIC probe only represents the direct/zapret tiers; on a
		// VPN/emergency rung it would false-fail (the tunnel carries no direct
		// HTTP/3 path). Enforce the LOT-3 footgun structurally.
		if step.ProbeType == dataplane.ProbeQUIC && step.StrategyClass != strategy.ClassDirect && step.StrategyClass != strategy.ClassZapret {
			return nil, fmt.Errorf("config: service %q step %d: probe_type quic is only valid on direct/zapret rungs, not %q", s.Name, i, step.StrategyClass)
		}
		out = append(out, registry.ChainStep{Position: i, State: step.State, StrategyClass: step.StrategyClass, StrategyID: step.StrategyID, ProbeType: step.ProbeType, ProbeTarget: step.ProbeTarget})
	}
	return out, nil
}

// injectBuiltinPools adds the standard pools (vpn_url_test, vpn_url_test_udp,
// emergency_pool) that a VPN/emergency chain step, subscription-via-pool, or
// device policy REFERENCES but the config never declared. The builtin categories
// name exactly these three, so without this a config that relies on category
// defaults (`category: streaming`) would generate empty url-test groups unless it
// also hand-copied the three pool blocks that pools.Builtin already provides. A
// pool the config declares itself is authoritative and never overwritten; a
// referenced name that is not one of the builtins is left alone (it may be a
// user pool declared elsewhere, or a genuine typo the generator surfaces).
func injectBuiltinPools(set *pools.Set, reg *registry.Registry, subViaPool string, devices []registry.Device) {
	referenced := map[string]bool{}
	for _, svc := range reg.Services {
		for _, step := range svc.Chain {
			if step.StrategyClass != strategy.ClassVPN && step.StrategyClass != strategy.ClassEmergency {
				continue
			}
			if step.StrategyID != "" {
				referenced[step.StrategyID] = true
			}
		}
	}
	if subViaPool != "" {
		referenced[subViaPool] = true
	}
	for _, d := range devices {
		if d.Policy != "" {
			referenced[d.Policy] = true
		}
	}
	builtin := pools.Builtin()
	for name := range referenced {
		if _, have := set.Pools[name]; have {
			continue
		}
		if p, ok := builtin.Pools[name]; ok {
			set.Pools[name] = p
		}
	}
}

func buildPools(in map[string]poolYAML) (*pools.Set, error) {
	set := &pools.Set{Pools: make(map[string]pools.Pool, len(in))}
	for name, p := range in {
		for _, c := range p.Filter.Caps {
			if c != pools.CapTCP && c != pools.CapUDPNative {
				return nil, fmt.Errorf("config: pool %q: unknown cap %q", name, c)
			}
		}
		for field, v := range map[string]string{"interval": p.Interval, "idle_timeout": p.IdleTimeout} {
			if v != "" {
				if _, err := time.ParseDuration(v); err != nil {
					return nil, fmt.Errorf("config: pool %q: %s %q: %w", name, field, v, err)
				}
			}
		}
		set.Pools[name] = pools.Pool{
			Name:        name,
			Type:        p.Type,
			Warmup:      p.Warmup,
			Interval:    p.Interval,
			IdleTimeout: p.IdleTimeout,
			Filter: pools.Filter{
				Caps:             p.Filter.Caps,
				TagsInclude:      p.Filter.TagsInclude,
				TagsExclude:      p.Filter.TagsExclude,
				CountriesInclude: lowerAll(p.Filter.CountriesInclude),
				CountriesExclude: lowerAll(p.Filter.CountriesExclude),
			},
		}
	}
	return set, nil
}

func validState(s string) bool {
	switch s {
	case registry.StatePreferred, registry.StateAltZapret, registry.StateVPN,
		registry.StateEmergency, registry.StateBroken, registry.StateLocked:
		return true
	}
	return false
}

func lowerAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

func validProfile(p string) bool {
	switch p {
	case "", "general", "voice", "streaming", "gaming":
		return true
	}
	return false
}

func validProbeType(t string) bool {
	switch t {
	case "", dataplane.ProbeHTTP, dataplane.ProbeTCP, dataplane.ProbeSTUN, dataplane.ProbeQUIC:
		return true
	}
	return false
}

func validClass(c string) bool {
	switch c {
	case strategy.ClassZapret, strategy.ClassVPN, strategy.ClassEmergency, strategy.ClassDirect:
		return true
	}
	return false
}
