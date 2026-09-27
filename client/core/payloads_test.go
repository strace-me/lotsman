package core

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

func recipe(id string, args ...string) strategycat.Recipe {
	return strategycat.Recipe{ID: id, NfqwsArgs: args}
}

func TestUsablePayloadRecipesDropsTheUnavailableOnes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.bin"), []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	in := []strategycat.Recipe{
		recipe("no-payload", "--dpi-desync=multisplit"),
		recipe("have", "--dpi-desync-split-seqovl-pattern=present.bin"),
		recipe("missing", "--dpi-desync-split-seqovl-pattern=absent.bin"),
	}

	got := usablePayloadRecipes(in, dir, slog.New(slog.DiscardHandler))

	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []string{"no-payload", "have"}
	if !slices.Equal(ids, want) {
		t.Errorf("kept %v, want %v — a recipe naming a payload this host lacks makes "+
			"nfqws fail to start, which reads as 'the desync does not work'", ids, want)
	}
}

func TestUsablePayloadRecipesKeepsAllWhenNoDirIsKnown(t *testing.T) {
	in := []strategycat.Recipe{recipe("x", "--dpi-desync-split-seqovl-pattern=whatever.bin")}
	if got := usablePayloadRecipes(in, "", slog.New(slog.DiscardHandler)); len(got) != 1 {
		t.Error("with no payload dir there is nothing to verify against; dropping every " +
			"recipe would disable the desync entirely")
	}
}

func TestAbsolutizePayloads(t *testing.T) {
	args := []string{
		"--new",
		"--hostlist-domains=youtube.com",
		"--dpi-desync-split-seqovl-pattern=tls_clienthello.bin",
		"--dpi-desync-fake-tls=/already/absolute.bin",
	}
	got := absolutizePayloads(args, "/opt/zapret/fake")
	want := []string{
		"--new",
		"--hostlist-domains=youtube.com",
		"--dpi-desync-split-seqovl-pattern=/opt/zapret/fake/tls_clienthello.bin",
		"--dpi-desync-fake-tls=/already/absolute.bin",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestAbsolutizePayloadsLeavesArgsAloneWithoutADir(t *testing.T) {
	args := []string{"--dpi-desync-split-seqovl-pattern=x.bin"}
	if got := absolutizePayloads(args, ""); !slices.Equal(got, args) {
		t.Errorf("got %v, want the args unchanged", got)
	}
}

func TestMissingPayloadNamesTheFileItWants(t *testing.T) {
	// The dropped recipe is indistinguishable from one that never worked unless
	// the log says WHICH FILE was absent — that is the only actionable thing in it.
	dir := t.TempDir()
	present := filepath.Join(dir, "quic_initial_default.bin")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	in := []strategycat.Recipe{
		recipe("missing-payload", "--dpi-desync-split-seqovl-pattern=tls_clienthello_absent.bin"),
	}
	if got := usablePayloadRecipes(in, dir, log); len(got) != 0 {
		t.Fatalf("recipe with an absent payload was kept: %#v", got)
	}
	text := buf.String()
	for _, want := range []string{"level=WARN", "tls_clienthello_absent.bin", "UNAVAILABLE"} {
		if !strings.Contains(text, want) {
			t.Errorf("warning is missing %q: %s", want, text)
		}
	}
}
