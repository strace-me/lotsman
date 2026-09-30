package configv2

import (
	"fmt"
	"strings"
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
	Version int `yaml:"version"`
	// TargetVersion is the sing-box release this document is GENERATED FOR. It
	// gates version-specific node features: a hy2 node carrying gecko obfs needs
	// 1.14, and generating without a target runs at the 1.12.17 baseline, which
	// SILENTLY DROPS the obfs.
	//
	// This field is here despite singbox_version being classed as a deployment
	// default, because that classification turned out to be wrong in a way that
	// costs data: the byte-for-byte comparison of a v1-generated and a v2-generated
	// config of the live r5s file found exactly one difference, and it was a node
	// that had lost its obfs. A knob that changes what existing nodes ARE is not a
	// default, it is a declaration.
	TargetVersion string `yaml:"target_version,omitempty"`
	// AppliedMigrations records which migrations have already run against this
	// document, by id and in order.
	//
	// It exists so a MIGRATION can be a condition of a RELEASE rather than a thing an
	// operator is expected to remember: a document that is missing a required
	// migration fails at load with the id of what to run, instead of starting and
	// quietly behaving like an older schema. The order is kept because a migration
	// that depends on an earlier one is a real dependency, and a set would lose it.
	AppliedMigrations []string              `yaml:"applied_migrations,omitempty"`
	Proxy             Proxy                 `yaml:"proxy"`
	Desync            Desync                `yaml:"desync"`
	Runtime           Runtime               `yaml:"runtime"`
	DataSources       map[string]DataSource `yaml:"data_sources"`
	Lists             Lists                 `yaml:"lists"`
	Pools             map[string]Pool       `yaml:"pools"`
	Devices           []Device              `yaml:"devices"`
	Subscriptions     []Subscription        `yaml:"subscriptions"`
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
	Interval    Duration `yaml:"interval,omitempty"`
	IdleTimeout Duration `yaml:"idle_timeout,omitempty"`
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
	// Capture is the port set this instance diverts to its queue, the nfqws
	// equivalent of "which traffic this process sees". It is NOT optional in
	// practice: an instance with no capture installs an nft rule that matches
	// nothing, so the desync engine is silently absent while every status read
	// still reports healthy. The live cutover found that the schema had no such
	// field, so the projection produced exactly that — a desync that captured
	// nothing and reported fine.
	//
	// Queue number is deliberately NOT a field: it is derived from the instance's
	// POSITION, so the first one keeps the deployment's base and adding an instance
	// cannot collide with a manually chosen number.
	Capture Capture `yaml:"capture"`
	// Connbytes limits the first N packets per connection; 0 = the whole stream.
	Connbytes int `yaml:"connbytes,omitempty"`
}

// Capture is a port set, stringified. A YAML item may be an int (443) or a range
// string ("50000-50100"), so the v2 form is []string and the same normalisation as
// v1 applies.
type Capture struct {
	TCP []string `yaml:"tcp,omitempty"`
	UDP []string `yaml:"udp,omitempty"`
}

type Runtime struct {
	Mode            string   `yaml:"mode"`
	ProbeInterval   Duration `yaml:"probe_interval,omitempty"`
	RefreshInterval Duration `yaml:"refresh_interval,omitempty"`
	WAN             string   `yaml:"wan"`
	StateDir        string   `yaml:"state_dir"`
	RuntimeDir      string   `yaml:"runtime_dir"`
}

// DataSource is where a named list's CONTENT comes from.
type DataSource struct {
	Type string `yaml:"type"`
	// Repo is the GitHub repository whose release carries the bundle, for the
	// v2ray-srs type. It is the reason a v2 document can be authoritative about its
	// own rule-sets: with the repo here, the binary does not need a flag that says
	// one thing while the config says another.
	Repo string `yaml:"repo"`
	// Pin is a release tag. Empty means track latest, which runs under the shrink
	// guard — a release that lost rule-sets is refused rather than applied.
	Pin     string `yaml:"pin,omitempty"`
	Geosite string `yaml:"geosite"`
	Geoip   string `yaml:"geoip"`
	// Refresh is how often Lotsman itself re-reads this source. It is what replaces
	// the separate cron that used to do the fetching: the period belongs to the
	// thing it refreshes, and a document that declares a source without a period
	// gets the global default rather than never being refreshed.
	Refresh        Duration `yaml:"refresh,omitempty"`
	VerifyChecksum bool     `yaml:"verify_checksum"`
}

type Lists struct {
	Domains map[string]List `yaml:"domains"`
	IPs     map[string]List `yaml:"ips"`
}

type List struct {
	// SRSURL is a compiled sing-box rule-set that sing-box fetches ITSELF, by URL.
	//
	// It is the "pass it through" form: we neither download it nor compile it, and
	// the generated config carries a `remote` rule-set instead of a `local` one. That
	// matters when the rule-set is maintained upstream and its checksum churns faster
	// than a release cadence — re-downloading it to find out it changed is a cost
	// with no answer attached.
	//
	// Mutually exclusive with the materialised forms: a list is EITHER handed to
	// sing-box as a remote or resolved by us, never both, because the two would
	// disagree about membership with nothing to say which wins.
	SRSURL   string   `yaml:"srs_url,omitempty"`
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
	// An explicitly empty value means "unset", not "invalid". A hand-written file
	// with `interval:` and nothing after it is a reasonable thing to write, and
	// rejecting it teaches the operator to distrust the parser rather than to fill
	// the field in.
	if strings.TrimSpace(raw) == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Value() time.Duration { return time.Duration(d) }

// MarshalYAML emits a duration the way a human writes one ("5m"), not as the
// underlying nanosecond count.
//
// Without this the round trip is BROKEN rather than merely ugly: a document this
// package writes is a document it cannot read back, because "21600000000000" has
// no unit and ParseDuration rejects it. A migration that emits files nobody can
// load is worse than one that emits nothing, since it looks like it worked.
func (d Duration) MarshalYAML() (any, error) {
	if d == 0 {
		return "", nil
	}
	return time.Duration(d).String(), nil
}
