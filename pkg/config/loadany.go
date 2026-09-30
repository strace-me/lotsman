package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/strace-me/lotsman/pkg/configv2"

	"gopkg.in/yaml.v3"
)

// LoadAny reads ONE configuration file and decides its own schema from the
// `version` field, so a single -config reaches both the v1 document and the v2
// document.
//
// It exists because two schemas with two entry points is the shape that produced
// every "which file is authoritative" question: -config for one and -run-v2 for the
// other, both valid, neither obviously right. The version travels WITH the document
// instead of on the command line, which means the file cannot be handed to a binary
// that reads it as the other schema.
//
// Two paths remain, and neither is a second config: a v2 document's rules live in
// `rules/` beside it, and a v2 deployment's strategy bundles in the store. That is
// v2's SHAPE — one file per rule, because editing a rule must not reformat a
// hand-kept file — not a second document to keep in sync.
//
// The default is 1, and that direction is deliberate: a file that declares nothing is
// the old schema, and a file that has always been read as v1 keeps being read as v1.
// A file that declares a version this build does not know is an ERROR, because
// guessing produces a config that loads and behaves like something else.
func LoadAny(path, v2RulesDir, v2StoreRoot string) (*Config, []string, error) {
	cfg, _, gaps, err := LoadAnyDocument(path, v2RulesDir, v2StoreRoot)
	return cfg, gaps, err
}

// LoadAnyDocument is LoadAny plus the SOURCE DOCUMENT, which the runtime Config
// deliberately does not carry: `data_sources` says where content comes from, and
// the fetcher is its only consumer. Returning it here is what keeps that plumbing out
// of every call site — the alternative is a root that re-reads the file to recover
// something the loader already had.
func LoadAnyDocument(path, v2RulesDir, v2StoreRoot string) (*Config, *configv2.Configuration, []string, error) {
	// A v2 deployment is configuration.yaml + rules/ as a unit, so the directory is
	// the natural thing to be handed and the file is the natural thing to name. Both
	// resolve, and neither is a second document: the rules are v2's SHAPE (one file
	// per rule, so editing a rule does not reformat a hand-kept file), not a second
	// configuration to keep in sync.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if v2RulesDir == "" {
			v2RulesDir = filepath.Join(path, "rules")
		}
		path = filepath.Join(path, "configuration.yaml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	version, err := SchemaVersion(data)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	switch version {
	case 1:
		cfg, err := Parse(data)
		return cfg, nil, nil, err
	case 2:
		load, err := LoadV2(path, v2RulesDir, v2StoreRoot)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		projected, gaps, err := FromV2(load.Config, load.Rules)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		return projected, load.Config, gaps, nil
	default:
		return nil, nil, nil, fmt.Errorf("config declares version %d and this build reads 1 and 2. "+
			"The file is newer than the program: upgrade before editing, rather than letting a "+
			"newer document load and behave like an older one", version)
	}
}

// SchemaVersion reads ONLY the version field, without parsing the document, so the
// answer is available before a decision about how to read it has been made. Decoding
// the whole file first and then asking "which was it" is the same guess one step
// later.
func SchemaVersion(data []byte) (int, error) {
	var probe struct {
		Version *int `yaml:"version"`
	}
	// A non-strict decode on purpose: the point is to READ the version out of a
	// document whose schema we have not chosen yet, so unknown keys must not fail here.
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return 0, fmt.Errorf("not a readable configuration: %w", err)
	}
	if probe.Version == nil {
		return 1, nil // absent means the old schema
	}
	return *probe.Version, nil
}

// V2CompanionDirs are where a v2 document keeps everything that is not the document.
// They are derived, never configured separately, so there is no second place to point
// at and therefore no second file to be authoritative.
func V2CompanionDirs(configPath, rulesDir string) string {
	if rulesDir != "" {
		return rulesDir
	}
	return filepath.Join(filepath.Dir(configPath), "rules")
}
