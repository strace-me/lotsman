package nfqwsgen

import (
	"reflect"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

func recipe(args ...string) strategycat.Recipe { return strategycat.Recipe{NfqwsArgs: args} }

func TestComposeScopesDomainsPerBlock(t *testing.T) {
	blocks := []Block{
		{Service: "discord", Domains: []string{"discord.com", "discord.media"},
			Recipe: recipe("--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=multisplit")},
		{Service: "gaming", Domains: []string{"battle.net"},
			Recipe: recipe("--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake")},
	}
	got := Compose(blocks)
	want := []string{
		"--new", "--filter-tcp=443", "--hostlist-domains=discord.com,discord.media", "--dpi-desync=multisplit",
		"--new", "--filter-tcp=443", "--hostlist-domains=battle.net", "--dpi-desync=fake",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose =\n  %v\nwant\n  %v", got, want)
	}
	// every block is --new-separated; one per service.
	if n := strings.Count(strings.Join(got, " "), "--new"); n != 2 {
		t.Errorf("got %d --new blocks, want 2", n)
	}
}

func TestComposeSkipsEmpty(t *testing.T) {
	blocks := []Block{
		{Service: "nodomains", Domains: nil, Recipe: recipe("--dpi-desync=fake")},                     // no domains -> skip
		{Service: "norecipe", Domains: []string{"x.com"}, Recipe: recipe()},                           // empty recipe -> skip
		{Service: "ok", Domains: []string{"y.com"}, Recipe: recipe("--hostlist-domains={{DOMAINS}}")}, // kept
	}
	got := Compose(blocks)
	want := []string{"--new", "--hostlist-domains=y.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v (only the well-formed block)", got, want)
	}
}

func TestComposeEmpty(t *testing.T) {
	if got := Compose(nil); len(got) != 0 {
		t.Errorf("Compose(nil) = %v, want empty", got)
	}
}

// LOT-14/LOT-36: a block's Exclude domains are emitted as --hostlist-exclude-domains
// after its recipe args (raw-pass CDNs); a block without Exclude is unchanged.
func TestComposeEmitsExclude(t *testing.T) {
	blocks := []Block{
		{Service: "gaming-epic", Domains: []string{"fortnite.com"},
			Recipe:  recipe("--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"),
			Exclude: []string{"epicgames-download1.akamaized.net", "modules-cdn.eac-prod.on.epicgames.com"}},
	}
	got := strings.Join(Compose(blocks), " ")
	want := "--hostlist-exclude-domains=epicgames-download1.akamaized.net,modules-cdn.eac-prod.on.epicgames.com"
	if !strings.Contains(got, want) {
		t.Errorf("Compose missing exclude:\n  %s", got)
	}
	// no Exclude -> no exclude arg (byte-identical to before).
	plain := Compose([]Block{{Service: "x", Domains: []string{"y.com"}, Recipe: recipe("--hostlist-domains={{DOMAINS}}")}})
	if strings.Contains(strings.Join(plain, " "), "exclude") {
		t.Errorf("block without Exclude must emit no exclude arg, got %v", plain)
	}
}
