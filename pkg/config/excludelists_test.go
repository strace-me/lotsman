package config

import "testing"

// exclude_lists is the mirror of domain_lists, and it exists because an upstream
// bundle's exclude list is 112 domains of CDNs that work direct and break under
// desync. Pasted inline into every rule, such a list stops being maintained; named
// once, it can be fetched and kept fresh like any other pack.
func TestExcludeListsMergeIntoExcludeDomains(t *testing.T) {
	lists := map[string]Hostlist{
		"flowseal-exclude": {Name: "flowseal-exclude", Domains: []string{"download.epicgames.com", "*.Akamaized.NET"}},
	}
	svc := serviceYAML{
		Name:           "youtube",
		ExcludeDomains: []string{"own.example"},
		ExcludeLists:   []string{"flowseal-exclude"},
	}
	got, err := mergeExcludeLists(svc, lists)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"own.example": true, "download.epicgames.com": true, "akamaized.net": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d entries", got, len(want))
	}
	for _, d := range got {
		if !want[d] {
			// "*.Akamaized.NET" must arrive as "akamaized.net": the generator emits
			// these as suffixes, and a leading star matches nothing.
			t.Errorf("unexpected or unnormalised entry %q in %v", d, got)
		}
	}
}

// A typo must not silently desync the endpoints the operator was trying to spare.
func TestExcludeListsRejectAnUnknownName(t *testing.T) {
	_, err := mergeExcludeLists(serviceYAML{Name: "youtube", ExcludeLists: []string{"typo"}}, nil)
	if err == nil {
		t.Fatal("naming a pack that does not exist must be a config error")
	}
}
