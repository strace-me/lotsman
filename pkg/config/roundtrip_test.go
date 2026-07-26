package config

import "testing"

// TestDocumentRoundTrips guards the property the in-app configurator relies on: a
// valid config parses into an editable Document, re-serialises to YAML, and
// re-parses cleanly (structure → YAML → validate). `sample` is from config_test.go.
func TestDocumentRoundTrips(t *testing.T) {
	d, err := ParseDocument([]byte(sample))
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	y, err := d.YAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Parse(y); err != nil {
		t.Fatalf("re-parse of round-tripped YAML failed: %v\n%s", err, y)
	}
}
