package lists

import (
	"context"
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

type fakeProvider struct {
	values []byte
}

func (fakeProvider) Name() string { return "runetfreedom" }

func (p fakeProvider) Load(context.Context, Reference, subscription.Fetcher) ([][]byte, error) {
	return [][]byte{p.values}, nil
}

func TestLoadWithProvider(t *testing.T) {
	snapshot, err := (Definition{Name: "reddit", Kind: Domains, Source: "runetfreedom", Dataset: "geosite", Category: "reddit"}).LoadWith(context.Background(), Providers{
		"runetfreedom": fakeProvider{values: []byte("reddit.com\n")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Values) != 1 || snapshot.Values[0] != "reddit.com" {
		t.Fatalf("values = %#v", snapshot.Values)
	}
}

func TestLoadWithUnknownProvider(t *testing.T) {
	if _, err := (Definition{Name: "reddit", Kind: Domains, Source: "missing"}).LoadWith(context.Background(), nil, nil); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}
