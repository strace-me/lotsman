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
	c.zapExec.preset = &config.ZapretPreset{Name: "ALT12", Args: want}

	got := c.rankedCandidates("youtube", "", 3)
	if len(got) != 1 || got[0] != "ALT12" {
		t.Fatalf("one bundle serves every rule; there is nothing to rank: %v", got)
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
