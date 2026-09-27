package zapret

import (
	"strings"
	"testing"
)

// A generated launcher is a /bin/sh script, so an nfqws2 range carrying `<` is a
// REDIRECT to that shell, not an argument to the engine. An unquoted join turns a
// working strategy into a launcher that dies before nfqws2 starts — and a domain
// list is worse, because it expands.
func TestLauncherQuotesEngineRangesSoTheShellDoesNotEatThem(t *testing.T) {
	def := strategyFixture("z2-multi", "--lua-desync=multisplit:pos=midsld", "--out-range=s1<d1")
	out, err := RenderLauncher(def, LauncherOpts{FilterTCP: "443", Engine: "nfqws2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `'--out-range=s1<d1'`) {
		t.Errorf("an out-range was not quoted, so the shell sees a redirect:\n%s", out)
	}
}

func TestLauncherQuotesHostlistDomainsWithShellMetacharacters(t *testing.T) {
	// A domain is operator-supplied text. Unquoted it would expand; quoted, the
	// engine receives it verbatim and rejects a bad one itself.
	def := strategyFixture("alt12", "--dpi-desync=fake")
	out, err := RenderLauncher(def, LauncherOpts{
		FilterTCP: "443", Engine: "nfqws1", HostlistDomains: []string{"a.example", "bad;rm -rf /"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `'--hostlist-domains=a.example,bad;rm -rf /'`) {
		t.Errorf("a domain list was not quoted as ONE argument:\n%s", out)
	}
}

func TestShellQuoteHandlesQuotesAndEmpty(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"},
		{"--filter-tcp=443", "--filter-tcp=443"},
		{"", "''"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"$(id)", "'$(id)'"},
		{"`id`", "'`id`'"},
		{"a|b", "'a|b'"},
		{"a;b", "'a;b'"},
		{"s1<d1", "'s1<d1'"},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The generated file is the contract with procd; a plain argument must stay
// readable rather than wrapped in quotes, so an operator can still read the file.
func TestPlainArgsStayUnquoted(t *testing.T) {
	def := strategyFixture("alt12", "--dpi-desync=fake")
	out, err := RenderLauncher(def, LauncherOpts{FilterTCP: "443", Engine: "nfqws1", Fwmark: DesyncFwmark})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "'--dpi-desync=fake'") {
		t.Errorf("a plain argument was needlessly quoted:\n%s", out)
	}
	if !strings.Contains(out, "--dpi-desync-fwmark=0x40000000") {
		t.Errorf("fwmark missing:\n%s", out)
	}
}
