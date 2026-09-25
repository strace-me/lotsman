package strategystore

import (
	"errors"
	"testing"
)

type rejectingAdapter struct{}

func (rejectingAdapter) Engine() string        { return "nfqws2" }
func (rejectingAdapter) Validate(Bundle) error { return errors.New("test adapter rejection") }
func (rejectingAdapter) Prepare(Bundle, PrepareOptions) ([]string, error) {
	return nil, errors.New("test adapter rejection")
}

func TestValidateCatalogUsesEngineAdapter(t *testing.T) {
	bundle := Bundle{Manifest: Manifest{ID: "lua-ts", Engine: "nfqws2"}}
	if err := ValidateCatalog([]Bundle{bundle}, map[string]EngineAdapter{"nfqws2": rejectingAdapter{}}); err == nil {
		t.Fatal("adapter rejection was not propagated")
	}
}

func TestValidateCatalogRejectsMissingAdapter(t *testing.T) {
	bundle := Bundle{Manifest: Manifest{ID: "lua-ts", Engine: "nfqws2"}}
	if err := ValidateCatalog([]Bundle{bundle}, nil); err == nil {
		t.Fatal("missing engine adapter was accepted")
	}
}
