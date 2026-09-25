package lists

import (
	"context"
	"testing"
)

func TestSnapshotKeepsSourceMetadata(t *testing.T) {
	snapshot, err := (Definition{
		Name:     "example",
		Kind:     Domains,
		Source:   "runetfreedom",
		Dataset:  "geosite",
		Category: "example",
		Domains:  []string{"manual.example"},
	}).LoadWith(context.Background(), Providers{
		"runetfreedom": fakeProvider{values: []byte("provider.example\n")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Refs) != 2 {
		t.Fatalf("refs = %#v", snapshot.Refs)
	}
	if snapshot.Refs[0].Type != "provider" || snapshot.Refs[0].Location != "runetfreedom/geosite/example" {
		t.Errorf("provider ref = %#v", snapshot.Refs[0])
	}
	if snapshot.Refs[1].Type != "inline" {
		t.Errorf("inline ref = %#v", snapshot.Refs[1])
	}
	if len(snapshot.Values) != 2 {
		t.Fatalf("values = %#v", snapshot.Values)
	}
}
