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
		"--filter-tcp=443", "--hostlist-domains=discord.com,discord.media", "--dpi-desync=multisplit",
		"--new", "--filter-tcp=443", "--hostlist-domains=battle.net", "--dpi-desync=fake",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose =\n  %v\nwant\n  %v", got, want)
	}
	// --new DELIMITS profiles rather than prefixing them: nfqws creates the first
	// profile itself, so N blocks need N-1 delimiters. An extra leading one would
	// prepend a filterless profile that matches everything and desyncs nothing.
	if n := strings.Count(strings.Join(got, " "), "--new"); n != 1 {
		t.Errorf("got %d delimiters for 2 blocks, want 1", n)
	}
}

func TestComposeSkipsEmpty(t *testing.T) {
	blocks := []Block{
		{Service: "nodomains", Domains: nil, Recipe: recipe("--dpi-desync=fake")},                     // no domains -> skip
		{Service: "norecipe", Domains: []string{"x.com"}, Recipe: recipe()},                           // empty recipe -> skip
		{Service: "ok", Domains: []string{"y.com"}, Recipe: recipe("--hostlist-domains={{DOMAINS}}")}, // kept
	}
	got := Compose(blocks)
	// The kept block is the first one EMITTED even though two blocks preceded it,
	// so it takes no delimiter — the counter has to follow what was emitted, not
	// the input index.
	want := []string{"--hostlist-domains=y.com"}
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

// Bug-hunt: domains are validated before joining into the /bin/sh exec arg list —
// a malformed/shell-metachar domain (e.g. from a remote .srs) is dropped, not
// emitted into the script.
func TestComposeDropsMalformedDomains(t *testing.T) {
	blocks := []Block{
		{Service: "x", Domains: []string{"good.com", "bad domain", "evil;rm -rf", "ok.net"},
			Recipe: recipe("--hostlist-domains={{DOMAINS}}")},
	}
	got := strings.Join(Compose(blocks), " ")
	if !strings.Contains(got, "good.com,ok.net") {
		t.Errorf("valid domains must survive in order: %s", got)
	}
	if strings.Contains(got, "bad domain") || strings.Contains(got, "evil;rm") || strings.Contains(got, " rf") {
		t.Errorf("malformed/shell-metachar domain leaked into shell args: %s", got)
	}
}

func TestValidateDetectsDomainConflict(t *testing.T) {
	blocks := []Block{
		{Service: "youtube", Domains: []string{"youtube.com", "googlevideo.com"}, Recipe: recipe("--dpi-desync=multisplit")},
		{Service: "youtube-stream", Domains: []string{"googlevideo.com"}, Recipe: recipe("--dpi-desync=fake")}, // re-claims googlevideo.com
		{Service: "discord", Domains: []string{"discord.com"}, Recipe: recipe("--dpi-desync=fake")},
	}
	conflicts := Validate(blocks)
	if len(conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.Domain != "googlevideo.com" || len(c.Services) != 2 || c.Services[0] != "youtube" || c.Services[1] != "youtube-stream" {
		t.Errorf("conflict = %+v, want googlevideo.com claimed by [youtube youtube-stream] in order", c)
	}
}

func TestValidateCleanWhenDisjoint(t *testing.T) {
	blocks := []Block{
		{Service: "youtube", Domains: []string{"youtube.com"}, Recipe: recipe("--dpi-desync=multisplit")},
		{Service: "discord", Domains: []string{"discord.com"}, Recipe: recipe("--dpi-desync=fake")},
		{Service: "empty", Domains: []string{"x.com"}, Recipe: recipe()}, // no recipe -> not emitted -> ignored
	}
	if c := Validate(blocks); len(c) != 0 {
		t.Errorf("disjoint blocks should validate clean, got %+v", c)
	}
}

func TestComposeDoesNotLeadWithNew(t *testing.T) {
	// nfqws auto-creates the first profile, and a profile with an empty filter
	// matches every packet. Leading with --new therefore inserts a do-nothing
	// catch-all ahead of the real strategy; profiles being matched first-to-last
	// until the first match, nothing is ever desynced. This was verified live —
	// the strategy loaded cleanly, nfqws ran, and YouTube stayed blocked.
	one := Compose([]Block{{
		Service: "youtube",
		Domains: []string{"youtube.com"},
		Recipe:  strategycat.Recipe{NfqwsArgs: []string{"--filter-tcp=443", "--dpi-desync=multisplit"}},
	}})
	if len(one) == 0 || one[0] == "--new" {
		t.Fatalf("the first profile must NOT be preceded by --new, got %v", one)
	}

	// A second block still needs the delimiter, or both would merge into one profile.
	two := Compose([]Block{
		{Service: "a", Domains: []string{"a.com"}, Recipe: strategycat.Recipe{NfqwsArgs: []string{"--dpi-desync=multisplit"}}},
		{Service: "b", Domains: []string{"b.com"}, Recipe: strategycat.Recipe{NfqwsArgs: []string{"--dpi-desync=fake"}}},
	})
	if got := countArg(two, "--new"); got != 1 {
		t.Errorf("two blocks need exactly one delimiter between them, got %d in %v", got, two)
	}
}

func countArg(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}
