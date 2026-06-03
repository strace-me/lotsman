package aggregate

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParseList(t *testing.T) {
	raw := []byte(`
# comment line
youtube.com
  Discord.com   // inline comment
*.googlevideo.com
not a domain
ftp://bad.com
.leading-dot.net
duplicate.com
duplicate.com
`)
	domains, invalid := ParseList(raw)
	want := []string{"youtube.com", "discord.com", "*.googlevideo.com", ".leading-dot.net", "duplicate.com", "duplicate.com"}
	if !reflect.DeepEqual(domains, want) {
		t.Fatalf("parsed = %v\nwant   = %v", domains, want)
	}
	if invalid != 2 { // "not a domain" (space), "ftp://bad.com" (scheme)
		t.Errorf("invalid = %d, want 2", invalid)
	}
}

func TestNormalizeCIDR(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"66.22.196.0/22": {"66.22.196.0/22", true},
		"1.2.3.4":        {"1.2.3.4/32", true},
		"2001:db8::1":    {"2001:db8::1/128", true},
		"not-an-ip":      {"", false},
		"1.2.3.4/99":     {"", false},
		"":               {"", false},
	}
	for in, exp := range cases {
		got, ok := NormalizeCIDR(in)
		if ok != exp.ok || got != exp.want {
			t.Errorf("NormalizeCIDR(%q) = (%q,%v), want (%q,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestParseIPList(t *testing.T) {
	raw := []byte("66.22.196.0/22\n# comment\n1.2.3.4\nbad junk\n8.8.8.8  // dns\n")
	cidrs, invalid := ParseIPList(raw)
	want := []string{"66.22.196.0/22", "1.2.3.4/32", "8.8.8.8/32"}
	if len(cidrs) != 3 || cidrs[1] != "1.2.3.4/32" || cidrs[2] != "8.8.8.8/32" {
		t.Errorf("cidrs = %v, want %v", cidrs, want)
	}
	if invalid != 1 { // "bad junk"
		t.Errorf("invalid = %d, want 1", invalid)
	}
}

func TestMergeDedupExcludeSort(t *testing.T) {
	lists := [][]string{
		{"b.com", "a.com", "dup.com"},
		{"dup.com", "c.com", "blocked.com"},
	}
	res := Merge(lists, []string{"blocked.com"})
	want := []string{"a.com", "b.com", "c.com", "dup.com"}
	if !reflect.DeepEqual(res.Domains, want) {
		t.Fatalf("merged = %v, want %v", res.Domains, want)
	}
	if res.Sources != 2 || res.Excluded != 1 {
		t.Errorf("counts: sources=%d excluded=%d, want 2/1", res.Sources, res.Excluded)
	}
}

// fakeFetcher serves canned bytes or errors per URL.
type fakeFetcher struct {
	data map[string][]byte
	err  map[string]error
}

func (f fakeFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	if e := f.err[url]; e != nil {
		return nil, e
	}
	if d, ok := f.data[url]; ok {
		return d, nil
	}
	return nil, errors.New("404")
}

func TestManagerBuildMergesAndSkipsDeadSource(t *testing.T) {
	ff := fakeFetcher{
		data: map[string][]byte{
			"u://a": []byte("youtube.com\ndiscord.com\nnot a domain\n"), // junk line
			"u://b": []byte("discord.com\nrutracker.org\n"),             // discord dups
			"u://x": []byte("discord.com\n"),                            // exclude source
		},
		err: map[string]error{"u://dead": errors.New("refused")},
	}
	m := NewManager(ff)
	res, errs := m.Build(context.Background(),
		[]Source{{Name: "a", URL: "u://a"}, {Name: "dead", URL: "u://dead"}, {Name: "b", URL: "u://b"}},
		[]Source{{Name: "x", URL: "u://x"}}, // exclude discord.com
	)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1 (dead source)", errs)
	}
	want := []string{"rutracker.org", "youtube.com"} // discord excluded, deduped, sorted
	if !reflect.DeepEqual(res.Domains, want) {
		t.Fatalf("domains = %v, want %v", res.Domains, want)
	}
	if res.Excluded != 2 { // discord.com dropped once per source it appeared in (a + b)
		t.Errorf("excluded = %d, want 2", res.Excluded)
	}
	if res.Invalid != 1 { // "not a domain" (space) from source a
		t.Errorf("invalid = %d, want 1", res.Invalid)
	}
}
