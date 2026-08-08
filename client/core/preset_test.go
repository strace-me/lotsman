package core

import (
	"os"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

// The owner's question, made mechanical: run ALT12 whole and let sing-box feed it.
// In preset mode the engine gets the bundle's own argv, unchanged and uncomposed —
// because a nine-profile bundle rendered one profile per rule is a different
// strategy under the same name, which is exactly why ALT12 is refused today.
func TestPresetModeAppliesTheBundleVerbatim(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	want := []string{"--filter-tcp=443", "--hostlist=/opt/zapret/list-google.txt", "--dpi-desync=hostfakesplit"}
	c.zapExec.presets = []config.ZapretPreset{{Name: "ALT12", Args: want}}

	got := c.rankedCandidates("youtube", "", 3)
	if len(got) != 1 || got[0] != "ALT12" {
		t.Fatalf("the declared bundles are the candidates: %v", got)
	}
}

// And the mode must not quietly become the old one. A preset declared with no
// arguments is refused at config load rather than falling back to composing: a
// composed strategy and a preset both look like "nfqws is running", so the
// fallback would be invisible.
func TestAnEmptyPresetIsRefusedRatherThanIgnored(t *testing.T) {
	dir := t.TempDir()
	empty := dir + "/empty.args"
	if err := os.WriteFile(empty, []byte("# nothing but a comment\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := "services:\n  - name: web\n    probe_target: https://example.com/\n    domains: [example.com]\n" +
		"    chain:\n      - { state: PREFERRED, class: zapret }\n" +
		"zapret:\n  preset:\n    name: ALT12\n    args_file: " + empty + "\n"
	_, err := config.Parse([]byte(doc))
	if err == nil {
		t.Fatal("an empty preset was accepted, so the operator's instruction silently became a no-op")
	}
	if !strings.Contains(err.Error(), "no arguments") {
		t.Errorf("the refusal must name the cause, got %v", err)
	}
}

// The owner's ask, made mechanical: twenty-one Flowseal strategies become
// twenty-one candidates, and the prober tries them WHOLE instead of trying
// fragments of one. Bounded per pass like any other candidate list, because a
// lift costs an nft table and an engine start.
func TestEveryDeclaredPresetIsACandidate(t *testing.T) {
	var applied []string
	c := rotCore(t, &applied)
	for _, n := range []string{"ALT", "ALT2", "ALT12", "EXP", "SIMPLE FAKE"} {
		c.zapExec.presets = append(c.zapExec.presets,
			config.ZapretPreset{Name: n, Args: []string{"--filter-tcp=443", "--dpi-desync=fake"}})
	}
	got := c.rankedCandidates("youtube", "", 3)
	if len(got) != 3 {
		t.Fatalf("a pass is bounded to three lifts, got %d: %v", len(got), got)
	}
	all := c.rankedCandidates("youtube", "", 99)
	if len(all) != 5 {
		t.Errorf("every declared bundle must be reachable across passes, got %v", all)
	}
	// And the one just tried is excluded, so a rotation moves ON rather than
	// re-measuring the bundle that just failed.
	if next := c.rankedCandidates("youtube", "ALT12", 99); len(next) != 4 {
		t.Errorf("the failed bundle was offered again: %v", next)
	}
}
