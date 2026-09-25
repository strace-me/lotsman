package strategystore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Manifest struct {
	ID      string            `yaml:"id"`
	Name    string            `yaml:"name"`
	Engine  string            `yaml:"engine"`
	Args    string            `yaml:"args"`
	Assets  map[string]string `yaml:"assets,omitempty"`
	Notes   string            `yaml:"notes,omitempty"`
	Version string            `yaml:"version,omitempty"`
}

func (m Manifest) Validate() error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("invalid strategy id %q", m.ID)
	}
	if m.Engine == "" {
		return fmt.Errorf("strategy %q has no engine", m.ID)
	}
	if m.Args == "" {
		return fmt.Errorf("strategy %q has no args file", m.ID)
	}
	if filepath.IsAbs(m.Args) || filepath.Clean(m.Args) != m.Args {
		return fmt.Errorf("strategy %q args path must be relative: %q", m.ID, m.Args)
	}
	for kind, path := range m.Assets {
		if filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("strategy %q asset %q path must be relative: %q", m.ID, kind, path)
		}
	}
	return nil
}

func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest %q: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("manifest %q: %w", path, err)
	}
	return m, nil
}

func WriteManifest(path string, m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}
