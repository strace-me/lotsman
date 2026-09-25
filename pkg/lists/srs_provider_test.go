package lists

import (
	"context"
	"testing"
)

type fakeEntryDecompiler struct {
	domains []string
	cidrs   []string
}

func (fakeEntryDecompiler) Entries(string) ([]string, []string, error) {
	return []string{"example.com"}, []string{"10.0.0.0/24"}, nil
}

func TestSRSProviderLoadsDomains(t *testing.T) {
	provider := SRSProvider{ProviderName: "runetfreedom", LiveDir: "/var/lib/lotsman/rule-sets", Decompiler: fakeEntryDecompiler{}}
	payload, err := provider.Load(context.Background(), Reference{Kind: Domains, Dataset: "geosite", Category: "example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || string(payload.Data[0]) != "example.com" {
		t.Fatalf("data = %q", payload.Data)
	}
}

func TestSRSProviderLoadsIPs(t *testing.T) {
	provider := SRSProvider{ProviderName: "runetfreedom", LiveDir: "/var/lib/lotsman/rule-sets", Decompiler: fakeEntryDecompiler{}}
	payload, err := provider.Load(context.Background(), Reference{Kind: IPs, Dataset: "geoip", Category: "example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || string(payload.Data[0]) != "10.0.0.0/24" {
		t.Fatalf("data = %q", payload.Data)
	}
}
