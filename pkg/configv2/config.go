package configv2

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPath       = "/etc/lotsman/configuration.yaml"
	RulesDir          = "/etc/lotsman/rules"
	DefaultStateDir   = "/var/lib/lotsman"
	DefaultRuntimeDir = "/run/lotsman"
)

type Configuration struct {
	Version       int                   `yaml:"version"`
	Proxy         Proxy                 `yaml:"proxy"`
	Desync        Desync                `yaml:"desync"`
	Runtime       Runtime               `yaml:"runtime"`
	DataSources   map[string]DataSource `yaml:"data_sources"`
	Lists         Lists                 `yaml:"lists"`
	Pools         map[string]Pool       `yaml:"pools"`
	Devices       []Device              `yaml:"devices"`
	Subscriptions []Subscription        `yaml:"subscriptions"`
}

// Device is a per-LAN-client egress override: every source IP or CIDR listed
// here leaves through Policy, whatever the rules say.
//
// This is the OWNER'S escape hatch and it is deliberately coarse: a console that
// will not work on a Russian ISP goes to the tunnel, and that is the whole point.
// The treatment matrix says a device may not decide how a specific FLOW is
// handled — the mode is a property of the traffic, not of the source — and this
// type does not do that. It only moves the exit. Per-flow treatment belongs to
// rules.
type Device struct {
	Name    string   `yaml:"name"`
	Sources []string `yaml:"sources"`
	Policy  string   `yaml:"policy"` // "direct" | "block" | a pool name
}

type Pool struct {
	Type        string   `yaml:"type"`
	Caps        []string `yaml:"caps"`
	TagsInclude []string `yaml:"tags_include"`
	TagsExclude []string `yaml:"tags_exclude"`
	// CountriesInclude/Exclude filter by exit country, ISO-3166 alpha-2,
	// lower-cased on load. The exclude is the crude half of the monoculture
	// workaround: a second pool that volume rules ride instead of the shared one,
	// keeping them off RU exits and off a saturated exit (LOT-69). A node whose
	// country is unknown is never excluded — "smart location" nodes pass.
	CountriesInclude []string `yaml:"countries_include"`
	CountriesExclude []string `yaml:"countries_exclude"`
	// Limit is speedtest_top's member cap: how many of the fastest nodes to keep.
	// Ignored by url_test, which is a selector and not a ranking. 0 or >= the
	// candidate count means no narrowing.
	Limit int `yaml:"limit"`
	// MinCoverage is the fraction of candidates that must carry a measurement
	// before a speedtest_top pool narrows at all. Below it the pool keeps its
	// whole filter and says so. It exists because the measurement is INBOUND —
	// `observe` counts bytes per exit, so exits nobody is using are invisible — and
	// an unguarded ranking in LOT-69's own state keeps the single busy exit and
	// collapses the pool to one member, which reads as a successful narrowing.
	// 0 = the default of one half.
	MinCoverage float64  `yaml:"min_coverage"`
	Warmup      bool     `yaml:"warmup"`
	Interval    Duration `yaml:"interval"`
	IdleTimeout Duration `yaml:"idle_timeout"`
}

type Subscription struct {
	Name    string   `yaml:"name"`
	URL     string   `yaml:"url"`
	Format  string   `yaml:"format"`
	Tags    []string `yaml:"tags"`
	Enabled bool     `yaml:"enabled"`
	Expires string   `yaml:"expires"`
}

type Proxy struct {
	Engine string `yaml:"engine"`
	Mode   string `yaml:"mode"`
}

type Desync struct {
	Instances map[string]Instance `yaml:"instances"`
}

type Instance struct {
	Engine  string `yaml:"engine"`
	Enabled bool   `yaml:"enabled"`
	// Table is this instance's own nft table. It is per-instance on purpose: each
	// instance installs by deleting and replacing its table, so a shared name would
	// let one instance tear down another's rules. Defaults to
	// `inet nfqueue-<instance>`; set it explicitly when a deployment already owns
	// a table name.
	Table string `yaml:"table"`
}

type Runtime struct {
	Mode            string   `yaml:"mode"`
	ProbeInterval   Duration `yaml:"probe_interval"`
	RefreshInterval Duration `yaml:"refresh_interval"`
	WAN             string   `yaml:"wan"`
	StateDir        string   `yaml:"state_dir"`
	RuntimeDir      string   `yaml:"runtime_dir"`
}

type DataSource struct {
	Type           string   `yaml:"type"`
	Repo           string   `yaml:"repo"`
	Geosite        string   `yaml:"geosite"`
	Geoip          string   `yaml:"geoip"`
	Refresh        Duration `yaml:"refresh"`
	VerifyChecksum bool     `yaml:"verify_checksum"`
}

type Lists struct {
	Domains map[string]List `yaml:"domains"`
	IPs     map[string]List `yaml:"ips"`
}

type List struct {
	Source   string   `yaml:"source"`
	Dataset  string   `yaml:"dataset"`
	Category string   `yaml:"category"`
	Files    []string `yaml:"files"`
	URLs     []string `yaml:"urls"`
	Domains  []string `yaml:"domains"`
	CIDRs    []string `yaml:"cidrs"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Value() time.Duration { return time.Duration(d) }
