package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/strace-me/lotsman/pkg/configv2"

	"gopkg.in/yaml.v3"
)

// Load reads a configuration and everything it references, and projects it onto the
// runtime shape. It returns the runtime Config, the SOURCE document, and what the
// document did not say.
//
// The source document is returned rather than carried on Config because
// `data_sources` says where content comes from and the fetcher is its only consumer.
// Returning it here is what keeps that plumbing out of every call site — the
// alternative is a root re-reading the file to recover something the loader already had.
//
// configPath may be the DIRECTORY or the configuration.yaml inside it, because a
// deployment is the two as a unit and an operator has the directory.
func Load(configPath, rulesDir, storeRoot string) (*Config, *configv2.Configuration, []string, error) {
	if info, err := os.Stat(configPath); err == nil && info.IsDir() {
		rulesDir = filepath.Join(configPath, "rules")
		configPath = filepath.Join(configPath, "configuration.yaml")
	}
	if rulesDir == "" {
		rulesDir = filepath.Join(filepath.Dir(configPath), "rules")
	}
	// The version is read BEFORE the loader is chosen, and a version this build does
	// not know is an ERROR rather than a guess: guessing produces a config that loads
	// and behaves like something else, which is the failure a version field exists to
	// prevent. It travels with the document rather than on the command line, so the
	// file cannot be handed to a binary that reads it as an older schema.
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	version, err := SchemaVersion(data)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", configPath, err)
	}
	if version != 2 {
		return nil, nil, nil, fmt.Errorf("%s declares configuration version %d, and this build reads %d. "+
			"The file is older than the program: migrate it (`lotsmanctl v2-migrate`) before starting, "+
			"rather than letting an old document load and behave like a current one", configPath, version, 2)
	}
	load, err := LoadV2(configPath, rulesDir, storeRoot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", configPath, err)
	}
	projected, gaps, err := FromV2(load.Config, load.Rules)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", configPath, err)
	}
	return projected, load.Config, gaps, nil
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
		return 0, fmt.Errorf("no `version:` field: a configuration must declare version 2. " +
			"If this is a version-1 file, convert it with `lotsmanctl v2-migrate`")
	}
	return *probe.Version, nil
}

// RulesDirFor is where a document keeps everything that is not the document: the rules
// beside it, and the strategy bundles one directory up. They are derived, never
// configured separately, so there is no second place to point at and therefore no
// second file to be authoritative.
func RulesDirFor(configPath, rulesDir string) string {
	if rulesDir != "" {
		return rulesDir
	}
	return filepath.Join(filepath.Dir(configPath), "rules")
}
