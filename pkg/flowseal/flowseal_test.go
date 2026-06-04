package flowseal

import (
	"context"
	"errors"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.9.9a", "1.9.9a", 0},
		{"1.9.9a", "1.9.9", 1}, // suffix beats no-suffix
		{"1.9.9", "1.9.9a", -1},
		{"1.9.9a", "1.9.8c", 1}, // numeric dominates suffix
		{"1.9.9b", "1.9.9a", 1}, // suffix order
		{"1.10.0", "1.9.9a", 1}, // numeric, not lexical (10 > 9)
		{"v1.9.5", "1.9.5", 0},  // leading v tolerated
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseRelease(t *testing.T) {
	body := []byte(`{
		"tag_name": "1.9.9a",
		"assets": [
			{"name": "Source code", "browser_download_url": "u://src"},
			{"name": "zapret-discord-youtube-1.9.9a.zip", "browser_download_url": "u://zip"}
		]
	}`)
	rel, err := ParseRelease(body)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "1.9.9a" || rel.ZipURL != "u://zip" {
		t.Errorf("rel = %+v", rel)
	}
}

// fakeFetcher / fakeInstaller for updater tests.
type fakeFetcher struct{ data map[string][]byte }

func (f fakeFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	if d, ok := f.data[url]; ok {
		return d, nil
	}
	return nil, errors.New("404")
}

type fakeInstaller struct {
	current   string
	installed string // tag installed
}

func (f *fakeInstaller) CurrentVersion() string { return f.current }
func (f *fakeInstaller) Install(_ context.Context, rel Release, _ []byte) error {
	f.installed = rel.Tag
	return nil
}

func TestUpdaterInstallsWhenNewer(t *testing.T) {
	release := []byte(`{"tag_name":"1.9.9a","assets":[{"name":"x.zip","browser_download_url":"u://zip"}]}`)
	ff := fakeFetcher{data: map[string][]byte{releaseAPI: release, "u://zip": []byte("ZIPBYTES")}}
	inst := &fakeInstaller{current: "1.9.8c"}

	out, err := NewUpdater(ff, inst).CheckAndUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Updated || inst.installed != "1.9.9a" {
		t.Errorf("expected install of 1.9.9a, got out=%+v installed=%q", out, inst.installed)
	}
}

func TestUpdaterNoopWhenCurrent(t *testing.T) {
	release := []byte(`{"tag_name":"1.9.9a","assets":[{"name":"x.zip","browser_download_url":"u://zip"}]}`)
	ff := fakeFetcher{data: map[string][]byte{releaseAPI: release, "u://zip": []byte("Z")}}
	inst := &fakeInstaller{current: "1.9.9a"} // already latest

	out, err := NewUpdater(ff, inst).CheckAndUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Updated || inst.installed != "" {
		t.Errorf("expected no-op, got out=%+v installed=%q", out, inst.installed)
	}
}
