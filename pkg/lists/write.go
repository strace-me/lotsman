package lists

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/strace-me/lotsman/pkg/atomicfile"
)

const userAssetRoot = "/etc"

func WriteRepresentations(runtimeDir string, representations []Representation) error {
	clean := filepath.Clean(runtimeDir)
	if clean == userAssetRoot || strings.HasPrefix(clean, userAssetRoot+string(filepath.Separator)) {
		return fmt.Errorf("refusing to write generated lists into %s: use a runtime directory", clean)
	}
	for _, representation := range representations {
		if representation.Filename == "" || filepath.IsAbs(representation.Filename) {
			return fmt.Errorf("invalid generated filename %q", representation.Filename)
		}
		path := filepath.Join(clean, representation.Engine, representation.Filename)
		if err := atomicfile.Write(path, []byte(representation.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
