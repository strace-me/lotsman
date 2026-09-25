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
	Subscriptions []Subscription        `yaml:"subscriptions"`
}

type Pool struct {
	Type        string   `yaml:"type"`
	Caps        []string `yaml:"caps"`
	TagsInclude []string `yaml:"tags_include"`
	TagsExclude []string `yaml:"tags_exclude"`
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
