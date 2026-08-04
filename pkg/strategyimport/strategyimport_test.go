package strategyimport

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// A Flowseal batch file, shaped like the real ones: an invocation to skip past,
// caret continuations, %~dp0 paths, a global capture filter that is packaging
// rather than strategy, and two blocks separated by --new.
const flowsealBat = `@echo off
rem general strategy
start "zapret: general" /min "%~dp0winws.exe" ^
--wf-tcp=80,443 --wf-udp=443,50000-50100 ^
--filter-tcp=443 --hostlist="%~dp0lists\list-general.txt" ^
--dpi-desync=fake,multisplit --dpi-desync-split-pos=1 --dpi-desync-fooling=ts ^
--dpi-desync-repeats=6 --dpi-desync-fake-tls="%~dp0bin\tls_clienthello_www_google_com.bin" ^
--new ^
--filter-udp=443 --dpi-desync=fake --dpi-desync-fake-quic="%~dp0bin\quic_initial_www_google_com.bin"
`

// The router's own script: shell continuations and a variable holding the queue.
const routerSh = `#!/bin/sh
QNUM=$1
BIN=/opt/zapret/nfqws
FAKE=/opt/zapret-lotsman/fake
exec "$BIN" --qnum=$QNUM --daemon \
  --filter-tcp=443 --hostlist-domains=youtube.com,googlevideo.com \
  --dpi-desync=fake,multisplit --dpi-desync-split-pos=1 --dpi-desync-fooling=ts \
  --dpi-desync-repeats=6 --dpi-desync-fake-tls=$FAKE/tls_clienthello_www_google_com.bin
`

func TestBatBlocksAreSplitAndTheInvocationDropped(t *testing.T) {
	blocks := Blocks(Source{Name: "fs", Kind: KindBat, Body: []byte(flowsealBat)})
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %v", len(blocks), blocks)
	}
	joined := strings.Join(blocks[0], " ")
	if strings.Contains(joined, "winws.exe") || strings.Contains(joined, "start") {
		t.Errorf("invocation leaked into the arguments: %q", joined)
	}
	if !strings.Contains(strings.Join(blocks[1], " "), "--dpi-desync-fake-quic=") {
		t.Errorf("second block lost its quic payload: %v", blocks[1])
	}
}

// The same strategy written for Windows and for Linux must land on ONE recipe:
// that is the whole point of normalizing, and it is what lets a Windows bundle
// seed a Linux box.
func TestBatAndShellConvergeOnOneRecipe(t *testing.T) {
	res := Import([]Source{
		{Name: "Flowseal 1.10.0", Kind: KindBat, Body: []byte(flowsealBat)},
		{Name: "router alt12", Kind: KindShell, Body: []byte(routerSh)},
	})
	var tcp []string
	for _, r := range res.Recipes {
		if strings.Contains(strings.Join(r.NfqwsArgs, " "), "multisplit") {
			tcp = r.NfqwsArgs
			if r.Consensus != 2 {
				t.Errorf("consensus = %d, want 2 (both bundles ship it)", r.Consensus)
			}
			if !strings.Contains(r.Provenance, "Flowseal") || !strings.Contains(r.Provenance, "router") {
				t.Errorf("provenance lost a source: %q", r.Provenance)
			}
		}
	}
	if tcp == nil {
		t.Fatalf("the shared recipe is missing: %+v", res.Recipes)
	}
	got := strings.Join(tcp, " ")
	for _, want := range []string{
		"--dpi-desync=fake,multisplit",
		"--dpi-desync-fake-tls=tls_clienthello_www_google_com.bin", // path reduced to a bare name
		"--filter-tcp=443",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("normalized args missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"%~dp0", "--qnum", "--wf-tcp", "--daemon", "--hostlist=", "/opt/"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("normalized args still carry deployment detail %q:\n%s", unwanted, got)
		}
	}
}

// The host set is Lotsman's decision, taken per service from the routing model.
func TestHostlistDomainsBecomeAPlaceholder(t *testing.T) {
	args, err := Normalize([]string{"--hostlist-domains=youtube.com,ytimg.com", "--dpi-desync=fake"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "--hostlist-domains={{DOMAINS}}") {
		t.Errorf("domains not templated: %s", got)
	}
}

// Refusing whole is the point: a recipe stripped of its ipset selects different
// traffic than the one its author wrote, and the canary would then score a
// strategy that was never run.
func TestUnexpressibleRecipesAreRefusedNotRepaired(t *testing.T) {
	_, err := Normalize([]string{"--ipset=/opt/lists/ipset-all.txt", "--dpi-desync=fake"})
	if err == nil {
		t.Fatal("an --ipset recipe was accepted; it must be refused")
	}
	if !strings.Contains(err.Error(), "--ipset") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
}

func TestSkippedBlocksAreReportedNotSwallowed(t *testing.T) {
	src := Source{Name: "b", Kind: KindShell, Body: []byte(
		"nfqws --filter-tcp=443 --ipset=/opt/x.txt --dpi-desync=fake\n")}
	res := Import([]Source{src})
	if len(res.Recipes) != 0 {
		t.Errorf("got %d recipes, want 0", len(res.Recipes))
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Source != "b" {
		t.Fatalf("the skip was not reported: %+v", res.Skipped)
	}
}

// An unresolvable variable must reject the block rather than be guessed at or
// passed through as a literal dollar sign.
func TestUnresolvedVariableIsRejected(t *testing.T) {
	if _, err := Normalize([]string{"--dpi-desync=fake", "--dpi-desync-repeats=$REPEATS"}); err == nil {
		t.Fatal("a block with an unresolved variable was accepted")
	}
}

// A block of nothing but filters selects traffic; it does not mangle any.
func TestFilterOnlyBlockIsNotARecipe(t *testing.T) {
	if _, err := Normalize([]string{"--filter-tcp=443", "--hostlist-domains=x.com"}); err != ErrEmpty {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

// The knowledge base keys outcomes on the recipe ID, so an ID that moved when a
// bundle updated would silently discard everything learned about that strategy.
func TestIDIsStableAcrossSourcesAndSpellings(t *testing.T) {
	a, _ := Normalize([]string{"--filter-tcp=443", "--dpi-desync=fake", "--dpi-desync-fake-tls=/a/b/x.bin"})
	b, _ := Normalize([]string{`"--filter-tcp=443"`, "--qnum=200", "--dpi-desync=fake", `--dpi-desync-fake-tls=%~dp0bin\x.bin`})
	if recipeID(a) != recipeID(b) {
		t.Errorf("same strategy got two IDs:\n %s from %v\n %s from %v", recipeID(a), a, recipeID(b), b)
	}
	c, _ := Normalize([]string{"--filter-tcp=443", "--dpi-desync=multisplit"})
	if recipeID(a) == recipeID(c) {
		t.Error("different strategies collided on one ID")
	}
}

func TestMarkdownFencesAreRead(t *testing.T) {
	md := "Try this one:\n\n```\nnfqws --filter-udp=443 --dpi-desync=fake --dpi-desync-fake-quic=quic_initial_www_google_com.bin\n```\n"
	res := Import([]Source{{Name: "docs", Kind: KindMarkdown, Body: []byte(md)}})
	if len(res.Recipes) != 1 {
		t.Fatalf("got %d recipes, want 1 (%+v)", len(res.Recipes), res.Skipped)
	}
	if res.Recipes[0].Protocol != "quic" {
		t.Errorf("protocol = %q, want quic", res.Recipes[0].Protocol)
	}
}

func TestTechniquesAreDerivedFromTheArguments(t *testing.T) {
	got := Techniques([]string{
		"--dpi-desync=fake,multisplit", "--dpi-desync-split-seqovl=652",
		"--dpi-desync-fooling=ts", "--dpi-desync-repeats=6",
		"--dpi-desync-fake-tls=x.bin", "--dpi-desync-fake-tls-mod=rnd,sni=www.google.com",
	})
	for _, want := range []string{"fake", "multisplit", "seqovl", "fooling:ts", "repeats:6", "fake-tls", "fake-sni"} {
		if !contains(got, want) {
			t.Errorf("missing technique %q in %v", want, got)
		}
	}
}

// The ground truth for this whole package: testdata holds the strategy scripts
// actually deployed on the router, and the catalog holds the recipes a PERSON
// derived from those same files by hand. Reading them automatically must produce
// exactly what the person produced — otherwise the normalizer is inventing a
// recipe that no one has ever run.
func TestImportDirReproducesTheHandCuratedCatalog(t *testing.T) {
	res, err := ImportDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[string]string{}
	for _, r := range strategycat.Load() {
		catalog[strings.Join(r.NfqwsArgs, " ")] = r.ID
	}
	if len(res.Recipes) == 0 {
		t.Fatal("no recipes read from testdata")
	}
	for _, r := range res.Recipes {
		if _, ok := catalog[strings.Join(r.NfqwsArgs, " ")]; !ok {
			t.Errorf("imported a recipe the catalog does not have:\n  %s", strings.Join(r.NfqwsArgs, " "))
		}
	}
	// Every skip must be an --ipset block: those are the only two in these
	// scripts that select by IP set, and nothing else may be quietly dropped.
	for _, s := range res.Skipped {
		if !strings.Contains(s.Reason, "--ipset") {
			t.Errorf("unexpected skip (%s): %s\n  %s", s.Source, s.Reason, s.Args)
		}
	}
	// alt11 and alt12 are two editions of one bundle, so the recipes they share
	// must be recognised as shared rather than duplicated.
	var shared int
	for _, r := range res.Recipes {
		if r.Consensus > 1 {
			shared++
		}
	}
	if shared == 0 {
		t.Error("no recipe was recognised in both scripts; dedup across sources is not working")
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
