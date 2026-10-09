package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/strace-me/lotsman/pkg/configv2"
	"gopkg.in/yaml.v3"
)

// This file is the v1 READER, and it exists for exactly one caller: MigrateToV2.
//
// It is deliberately not the old parser. The old parser validated a v1 document into
// a runtime Config, and that is the second reader whose disagreement with the v2
// reader produced every "which file is authoritative" question this project has had.
// Nothing loads a v1 document to RUN it any more — `lotsmanctl v2-migrate` reads one
// to CONVERT it, and the runtime reads only v2.
//
// So it is a compatibility importer: it decodes the fields the migration needs, it
// does not validate, and it does not pretend to know what a running configuration is.
// Keeping it here rather than deleting it is what makes the migration a supported path
// instead of a one-shot command somebody runs once and loses.

type LegacyDocument struct {
	SingboxVersion string `yaml:"singbox_version"`
	// Every section below is carried by the migration. It is tempting to read only the
	// ones the first v2 draft had: the migration then "succeeds" and quietly drops a
	// split-DNS block, a uTLS fingerprint, a multiplex block — and a converted document
	// that loads is exactly what makes the loss invisible. The v1 reader is the only
	// place a v1 section is understood, so a section missing HERE is a section the
	// migration loses.
	DNS             *LegacyDNSSection   `yaml:"dns"`
	FakeIP          *LegacyFakeIP       `yaml:"fakeip"`
	Multiplex       *LegacyMultiplex    `yaml:"multiplex"`
	UTLSFingerprint string              `yaml:"utls_fingerprint"`
	SubViaPool      string              `yaml:"subscription_via_pool"`
	Strategies      []LegacyStrategy    `yaml:"strategies"`
	Devices         []LegacyDevice      `yaml:"devices"`
	Categories      map[string]struct{} `yaml:"categories"`
	Zapret          struct {
		Instances []LegacyInstance `yaml:"instances"`
		Preset    LegacyPreset     `yaml:"preset"`
	} `yaml:"zapret"`
	Pools         map[string]LegacyPool `yaml:"pools"`
	Subscriptions []LegacySubscription  `yaml:"subscriptions"`
	Hostlists     []LegacyHostlist      `yaml:"hostlists"`
	Services      []LegacyService       `yaml:"services"`
}

type LegacyPreset struct {
	Name     string `yaml:"name"`
	ArgsFile string `yaml:"args_file"`
	Dir      string `yaml:"dir"`
}

type LegacyDNSSection struct {
	Servers        []configv2.DNSSectionServer `yaml:"servers"`
	Direct         string                      `yaml:"direct"`
	Zapret         string                      `yaml:"zapret"`
	Final          string                      `yaml:"final"`
	Strategy       string                      `yaml:"strategy"`
	FakeIP         bool                        `yaml:"fakeip"`
	Failover       []string                    `yaml:"failover"`
	DirectFailover []string                    `yaml:"direct_failover"`
}

type LegacyFakeIP struct {
	Enabled    bool   `yaml:"enabled"`
	Inet4Range string `yaml:"inet4_range"`
	Inet6Range string `yaml:"inet6_range"`
	Resolver   string `yaml:"resolver"`
}

type LegacyMultiplex struct {
	Enabled        bool   `yaml:"enabled"`
	Protocol       string `yaml:"protocol"`
	MaxConnections int    `yaml:"max_connections"`
	MinStreams     int    `yaml:"min_streams"`
	Padding        bool   `yaml:"padding"`
	BrutalUp       int    `yaml:"brutal_up_mbps"`
	BrutalDown     int    `yaml:"brutal_down_mbps"`
}

type LegacyStrategy struct {
	ID         string   `yaml:"id"`
	Class      string   `yaml:"class"`
	NFQWSArgs  []string `yaml:"nfqws_args"`
	BlockTypes []string `yaml:"block_types"`
	Notes      string   `yaml:"notes"`
}

type LegacyDevice struct {
	Name    string   `yaml:"name"`
	Sources []string `yaml:"sources"`
	Policy  string   `yaml:"policy"`
}

type LegacyInstance struct {
	Name    string `yaml:"name"`
	Capture struct {
		TCP []any `yaml:"tcp"`
		UDP []any `yaml:"udp"`
	} `yaml:"capture"`
	Connbytes int `yaml:"connbytes"`
}

type LegacyPool struct {
	Type        string `yaml:"type"`
	Limit       int    `yaml:"limit"`
	Warmup      bool   `yaml:"warmup"`
	Interval    string `yaml:"interval"`
	IdleTimeout string `yaml:"idle_timeout"`
	Filter      struct {
		Caps             []string `yaml:"caps"`
		TagsInclude      []string `yaml:"tags_include"`
		TagsExclude      []string `yaml:"tags_exclude"`
		CountriesInclude []string `yaml:"countries_include"`
		CountriesExclude []string `yaml:"countries_exclude"`
	} `yaml:"filter"`
}

type LegacySubscription struct {
	Name    string   `yaml:"name"`
	URL     string   `yaml:"url"`
	Format  string   `yaml:"format"`
	Tags    []string `yaml:"tags"`
	Enabled bool     `yaml:"enabled"`
	Expires string   `yaml:"expires"`
}

type LegacyHostlist struct {
	Name string `yaml:"name"`
	Out  string `yaml:"out"`
	// The shrink guard carries because it is the thing that makes a hostlist worth
	// having: a mirror that answers 200 with an empty body is not a failure anyone
	// notices, and the symptom is a service that silently stops being desynced.
	MinKeepRatio float64  `yaml:"min_keep_ratio"`
	Sources      []string `yaml:"sources"`
	Exclude      []string `yaml:"exclude"`
	Domains      []string `yaml:"domains"`
}

type LegacyService struct {
	Name           string            `yaml:"name"`
	Disabled       bool              `yaml:"disabled"`
	Category       string            `yaml:"category"`
	ProbeType      string            `yaml:"probe_type"`
	ProbeTarget    string            `yaml:"probe_target"`
	VolumeTarget   string            `yaml:"volume_target"`
	VolumeTargets  []string          `yaml:"volume_targets"`
	VolumeBytes    int               `yaml:"volume_bytes"`
	RuleSets       []string          `yaml:"rule_sets"`
	Domains        []string          `yaml:"domains"`
	DomainLists    []string          `yaml:"domain_lists"`
	ExcludeLists   []string          `yaml:"exclude_lists"`
	ExcludeDomains []string          `yaml:"exclude_domains"`
	SpreadClients  []string          `yaml:"spread_clients"`
	IPs            []string          `yaml:"ips"`
	IPsFile        string            `yaml:"ips_file"`
	Sticky         *bool             `yaml:"sticky"`
	Profile        string            `yaml:"profile"`
	Static         bool              `yaml:"static"`
	EscalateAfter  int               `yaml:"escalate_after"`
	RecoverAfter   int               `yaml:"recover_after"`
	Priority       int               `yaml:"priority"`
	TLSFragment    bool              `yaml:"tls_fragment"`
	Chain          []LegacyChainStep `yaml:"chain"`
}

type LegacyChainStep struct {
	State       string `yaml:"state"`
	Class       string `yaml:"class"`
	StrategyID  string `yaml:"strategy_id"`
	ProbeType   string `yaml:"probe_type"`
	ProbeTarget string `yaml:"probe_target"`
}

// ParseLegacyDocument decodes a v1 document for migration.
//
// It is NOT strict, on purpose: a migration's job is to report what it carried and
// what it could not, and refusing the file on an unrecognised key would turn a partial
// conversion into no conversion at all. Anything the migration does not understand is
// reported in Warnings, which is the honest way to lose a field.
func ParseLegacyDocument(data []byte) (*LegacyDocument, error) {
	var d LegacyDocument
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(&d); err != nil {
		if err == io.EOF {
			return &d, nil // an empty document migrates to an empty one
		}
		return nil, fmt.Errorf("config: reading a v1 document to migrate: %w", err)
	}
	return &d, nil
}
