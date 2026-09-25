package lists

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeFetcher map[string][]byte

func (f fakeFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	return f[url], nil
}

func TestLoadDomains(t *testing.T) {
	file := filepath.Join(t.TempDir(), "domains.txt")
	if err := os.WriteFile(file, []byte("# list\nexample.com\n\nWWW.EXAMPLE.COM\nbroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (Definition{Name: "example", Kind: Domains, Files: []string{file}}).Load(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Values) != 2 || snapshot.Values[0] != "example.com" || snapshot.Values[1] != "www.example.com" {
		t.Fatalf("values = %#v", snapshot.Values)
	}
	if snapshot.Invalid != 1 {
		t.Fatalf("invalid = %d, want 1", snapshot.Invalid)
	}
}

func TestLoadIPs(t *testing.T) {
	snapshot, err := (Definition{Name: "steam", Kind: IPs, CIDRs: []string{"10.0.0.1", "10.0.0.0/24"}}).Load(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Values) != 2 || snapshot.Values[0] != "10.0.0.0/24" {
		t.Fatalf("values = %#v", snapshot.Values)
	}
}

func TestLoadURL(t *testing.T) {
	snapshot, err := (Definition{Name: "example", Kind: Domains, URLs: []string{"https://list.invalid/example"}}).Load(context.Background(), fakeFetcher{
		"https://list.invalid/example": []byte("example.com\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Values) != 1 || snapshot.Values[0] != "example.com" {
		t.Fatalf("values = %#v", snapshot.Values)
	}
}

func TestLoadURLRequiresFetcher(t *testing.T) {
	if _, err := (Definition{Name: "example", Kind: Domains, URLs: []string{"https://list.invalid/example"}}).Load(context.Background(), nil); err == nil {
		t.Fatal("URL list without fetcher was accepted")
	}
}
