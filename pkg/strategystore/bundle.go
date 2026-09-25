package strategystore

import (
	"fmt"
	"os"
	"path/filepath"
)

type Bundle struct {
	Root     string
	Manifest Manifest
	Args     Args
	Revision string
}

func LoadBundle(root string) (Bundle, error) {
	manifest, err := ReadManifest(filepath.Join(root, "strategy.yaml"))
	if err != nil {
		return Bundle{}, err
	}
	args, err := ReadArgs(filepath.Join(root, manifest.Args), manifest.Name, true)
	if err != nil {
		return Bundle{}, err
	}
	for kind, rel := range manifest.Assets {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); err != nil {
			return Bundle{}, fmt.Errorf("bundle %q asset %q: %w", manifest.ID, kind, err)
		}
	}
	revision, err := ContentRevision(root)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Root: root, Manifest: manifest, Args: args, Revision: revision}, nil
}

func WriteBundle(root string, manifest Manifest, args []string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := WriteManifest(filepath.Join(root, "strategy.yaml"), manifest); err != nil {
		return err
	}
	return WriteArgs(filepath.Join(root, manifest.Args), args)
}
