package lists

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const plainFile = `lists:
  - name: youtube
    length: 2
    rules:
      - "domain:youtube.com"
      - "full:youtu.be"
  - name: mixed
    length: 4
    rules:
      - "domain:ok.example"
      - "regexp:.*\\.ads\\.example"
      - "keyword:tracker"
  - name: nets
    length: 2
    rules:
      - "ip_cidr:10.0.0.0/24"
      - "ip_cidr:2001:db8::/32"
`

func plainSource(t *testing.T) *PlainProvider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "geosite.dat_plain.yml")
	if err := os.WriteFile(path, []byte(plainFile), 0o644); err != nil {
		t.Fatal(err)
	}
	return &PlainProvider{ProviderName: "community-plain", Path: path}
}

func TestPlainProviderServesDomains(t *testing.T) {
	provider := plainSource(t)
	payload, err := provider.Load(context.Background(), Reference{Kind: Domains, Dataset: "geosite", Category: "youtube"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := ParseValues(Domains, payload.Data[0])
	if strings.Join(values, ",") != "youtube.com,youtu.be" {
		t.Fatalf("values = %#v", values)
	}
	if payload.Skipped != 0 {
		t.Errorf("skipped = %d", payload.Skipped)
	}
}

func TestPlainProviderServesIPs(t *testing.T) {
	provider := plainSource(t)
	payload, err := provider.Load(context.Background(), Reference{Kind: IPs, Dataset: "geoip", Category: "nets"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := ParseValues(IPs, payload.Data[0])
	if strings.Join(values, ",") != "10.0.0.0/24,2001:db8::/32" {
		t.Fatalf("values = %#v", values)
	}
}

func TestPlainProviderReportsInexpressibleRules(t *testing.T) {
	provider := plainSource(t)
	payload, err := provider.Load(context.Background(), Reference{Kind: Domains, Dataset: "geosite", Category: "mixed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := ParseValues(Domains, payload.Data[0])
	if len(values) != 1 || values[0] != "ok.example" {
		t.Fatalf("values = %#v", values)
	}
	// The regex and keyword rules must be reported, not silently dropped: a
	// category that quietly lost them is indistinguishable from one that had none.
	if payload.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2", payload.Skipped)
	}
}

func TestPlainProviderRejectsUnknownCategory(t *testing.T) {
	provider := plainSource(t)
	if _, err := provider.Load(context.Background(), Reference{Kind: Domains, Dataset: "geosite", Category: "ghost"}, nil); err == nil {
		t.Fatal("an unknown category was accepted")
	}
}

func TestPlainProviderParsesTheFileOnce(t *testing.T) {
	provider := plainSource(t)
	provider.mu.Lock()
	before := provider.parses
	provider.mu.Unlock()
	for _, category := range []string{"youtube", "mixed", "nets"} {
		if _, err := provider.Load(context.Background(), Reference{Kind: Domains, Dataset: "geosite", Category: category}, nil); err != nil {
			t.Fatal(err)
		}
	}
	provider.mu.Lock()
	parses := provider.parses
	provider.mu.Unlock()
	if parses != before+1 {
		t.Fatalf("parsed %d times for 3 categories; the index is not being reused", parses-before)
	}
}

func TestPlainProviderReindexesWhenTheFileChanges(t *testing.T) {
	provider := plainSource(t)
	ref := Reference{Kind: Domains, Dataset: "geosite", Category: "youtube"}
	first, err := provider.Load(context.Background(), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(plainFile, `      - "full:youtu.be"`, `      - "full:youtu.be"
      - "domain:added.example"`, 1)
	if err := os.WriteFile(provider.Path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := provider.Load(context.Background(), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Data[0]) == len(second.Data[0]) {
		t.Fatal("the provider served a stale index after the file changed")
	}
}
