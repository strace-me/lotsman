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

func (p fakeProvider) Load(context.Context, Reference, subscription.Fetcher) (Payload, error) {
	return Payload{Data: [][]byte{p.values}}, nil
}

func TestLoadWithProvider(t *testing.T) {
	snapshot, err := (Definition{Name: "example", Kind: Domains, Source: "runetfreedom", Dataset: "geosite", Category: "example"}).LoadWith(context.Background(), Providers{
		"runetfreedom": fakeProvider{values: []byte("example.com\n")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Values) != 1 || snapshot.Values[0] != "example.com" {
		t.Fatalf("values = %#v", snapshot.Values)
	}
}

func TestLoadWithUnknownProvider(t *testing.T) {
	if _, err := (Definition{Name: "example", Kind: Domains, Source: "missing"}).LoadWith(context.Background(), nil, nil); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}
