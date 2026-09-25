package strategystore

import "fmt"

func ValidateCatalog(bundles []Bundle, adapters map[string]EngineAdapter) error {
	for _, bundle := range bundles {
		adapter, ok := adapters[bundle.Manifest.Engine]
		if !ok {
			return fmt.Errorf("strategy %q has no engine adapter for %q", bundle.Manifest.ID, bundle.Manifest.Engine)
		}
		if err := adapter.Validate(bundle); err != nil {
			return fmt.Errorf("strategy %s/%s: %w", bundle.Manifest.Engine, bundle.Manifest.ID, err)
		}
	}
	return nil
}
