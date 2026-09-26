package lists

import (
	"strings"
	"testing"
)

func sources() []SourceConfig {
	return []SourceConfig{
		{
			Name:       "runetfreedom",
			LiveDir:    "/var/lib/lotsman/rule-sets/v2ray",
			Categories: []string{"geosite:youtube", "geosite:discord", "geosite:ru-blocked"},
			Decompiler: fakeEntryDecompiler{},
		},
		{
			Name:       "russia-blocked-geosite",
			LiveDir:    "/var/lib/lotsman/rule-sets/blocked",
			Categories: []string{"geosite:ru-blocked"},
			Decompiler: fakeEntryDecompiler{},
		},
	}
}

func TestRegistryKeepsSourcesApart(t *testing.T) {
	registry, err := NewRegistry(sources())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(registry.Names(), ","); got != "runetfreedom,russia-blocked-geosite" {
		t.Fatalf("names = %q", got)
	}
	// ru-blocked is in BOTH: each source must answer for its own copy, which is
	// why the provider carries the source's LiveDir rather than a shared one.
	for _, name := range registry.Names() {
		provider, err := registry.Resolve(name, "geosite", "ru-blocked")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, ok := provider.(SRSProvider); !ok || !strings.HasPrefix(got.LiveDir, "/var/lib/lotsman/rule-sets/") {
			t.Fatalf("%s resolved to %#v", name, provider)
		}
	}
}

func TestRegistryRefusesACategoryFromAnotherSource(t *testing.T) {
	registry, err := NewRegistry(sources())
	if err != nil {
		t.Fatal(err)
	}
	// geosite:youtube exists in runetfreedom only. Asking the blocked source for
	// it must fail rather than quietly serve runetfreedom's copy.
	if _, err := registry.Resolve("russia-blocked-geosite", "geosite", "youtube"); err == nil {
		t.Fatal("a category leaked from one source into another")
	}
	if _, err := registry.Resolve("runetfreedom", "geosite", "steam"); err == nil {
		t.Fatal("an undeclared category was accepted")
	}
	if _, err := registry.Resolve("ghost", "geosite", "youtube"); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

func TestRegistryRejectsDuplicateAndUnnamedSources(t *testing.T) {
	if _, err := NewRegistry([]SourceConfig{{Name: ""}}); err == nil {
		t.Fatal("an unnamed source was accepted")
	}
	if _, err := NewRegistry(append(sources(), sources()[0])); err == nil {
		t.Fatal("a duplicate source name was accepted")
	}
}

func TestRegistryWithoutCategoriesAcceptsAnything(t *testing.T) {
	registry, err := NewRegistry([]SourceConfig{{Name: "any", LiveDir: "/tmp", Decompiler: fakeEntryDecompiler{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("any", "geosite", "whatever"); err != nil {
		t.Fatalf("a source that declares no categories must not pre-reject: %v", err)
	}
}
