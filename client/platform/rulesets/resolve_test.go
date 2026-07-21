package rulesets

import (
	"slices"
	"testing"
)

func TestParseDecompiled(t *testing.T) {
	// Shape verified against `sing-box rule-set decompile` 1.13.14.
	const geosite = `{
	  "version": 1,
	  "rules": [
	    {
	      "domain": ["discord-attachments-uploads-prd.storage.googleapis.com"],
	      "domain_suffix": ["dis.gd", "discord.com"]
	    }
	  ]
	}`
	got, err := ParseDecompiled([]byte(geosite))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"discord-attachments-uploads-prd.storage.googleapis.com", "dis.gd", "discord.com"}
	if !slices.Equal(got, want) {
		t.Errorf("domains = %v, want %v", got, want)
	}
}

func TestParseDecompiledGeoIPHasNoDomains(t *testing.T) {
	// A geoip rule-set carries ip_cidr only. nfqws matches by hostname, so an
	// empty domain set is the CORRECT answer here — not a failure.
	const geoip = `{"version":1,"rules":[{"ip_cidr":["1.1.1.0/24","8.8.8.0/24"]}]}`
	got, err := ParseDecompiled([]byte(geoip))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("geoip rule-set must yield no domains, got %v", got)
	}
}

func TestParseDecompiledMultipleRules(t *testing.T) {
	const multi = `{"version":1,"rules":[
	  {"domain_suffix":["a.com"]},
	  {"domain":["b.example"],"domain_suffix":["c.net"]}
	]}`
	got, err := ParseDecompiled([]byte(multi))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"a.com", "b.example", "c.net"}
	if !slices.Equal(got, want) {
		t.Errorf("domains = %v, want %v", got, want)
	}
}

func TestParseDecompiledRejectsGarbage(t *testing.T) {
	if _, err := ParseDecompiled([]byte("not json")); err == nil {
		t.Error("expected an error on non-JSON input")
	}
}
