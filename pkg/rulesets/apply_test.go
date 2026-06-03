package rulesets

import (
	"os"
	"path/filepath"
	"testing"
)

// pathFor mirrors the sing-box layout: geosite-* under rule-set-geosite/, etc.
func testPathFor(tag string) (string, bool) {
	switch {
	case len(tag) > 8 && tag[:8] == "geosite-":
		return "rule-set-geosite/" + tag + ".srs", true
	case len(tag) > 6 && tag[:6] == "geoip-":
		return "rule-set-geoip/" + tag + ".srs", true
	}
	return "", false
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApplySwapsOnlyPlannedTags(t *testing.T) {
	src, live := t.TempDir(), t.TempDir()
	// Candidate release has new content for youtube + discord.
	writeFile(t, src, "rule-set-geosite/geosite-youtube.srs", "NEW-YT")
	writeFile(t, src, "rule-set-geosite/geosite-discord.srs", "NEW-DC")
	// Live has old youtube + a discord we intend to KEEP (shrank).
	writeFile(t, live, "rule-set-geosite/geosite-youtube.srs", "OLD-YT")
	writeFile(t, live, "rule-set-geosite/geosite-discord.srs", "OLD-DC")

	p := Plan{
		Swap:    []string{"geosite-youtube"},
		KeepOld: map[string]ShrinkInfo{"geosite-discord": {Prev: 200, Next: 50}},
	}
	changed, err := Apply(p, src, live, testPathFor)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(changed) != 1 || changed[0] != "geosite-youtube" {
		t.Fatalf("changed = %v, want [geosite-youtube]", changed)
	}
	// youtube updated, discord untouched (kept old).
	if got, _ := os.ReadFile(filepath.Join(live, "rule-set-geosite/geosite-youtube.srs")); string(got) != "NEW-YT" {
		t.Errorf("youtube = %q, want NEW-YT", got)
	}
	if got, _ := os.ReadFile(filepath.Join(live, "rule-set-geosite/geosite-discord.srs")); string(got) != "OLD-DC" {
		t.Errorf("discord = %q, want OLD-DC (kept)", got)
	}
}

func TestApplySkipsUnchanged(t *testing.T) {
	src, live := t.TempDir(), t.TempDir()
	writeFile(t, src, "rule-set-geosite/geosite-youtube.srs", "SAME")
	writeFile(t, live, "rule-set-geosite/geosite-youtube.srs", "SAME")

	changed, err := Apply(Plan{Swap: []string{"geosite-youtube"}}, src, live, testPathFor)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("identical content must not count as changed, got %v", changed)
	}
}

func TestApplyCreatesMissingDestDir(t *testing.T) {
	src, live := t.TempDir(), t.TempDir() // live has no rule-set-geoip/ yet
	writeFile(t, src, "rule-set-geoip/geoip-telegram.srs", "TG")

	changed, err := Apply(Plan{Swap: []string{"geoip-telegram"}}, src, live, testPathFor)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %v, want one", changed)
	}
	if got, _ := os.ReadFile(filepath.Join(live, "rule-set-geoip/geoip-telegram.srs")); string(got) != "TG" {
		t.Errorf("geoip-telegram = %q, want TG", got)
	}
}

func TestApplyRefusesUnusablePlan(t *testing.T) {
	src, live := t.TempDir(), t.TempDir()
	p := Plan{Unusable: true, Missing: []string{"geosite-gone"}}
	if _, err := Apply(p, src, live, testPathFor); err == nil {
		t.Fatal("Apply must refuse an Unusable plan")
	}
}

func TestApplyErrorsOnMissingSource(t *testing.T) {
	src, live := t.TempDir(), t.TempDir() // src empty -> read fails
	if _, err := Apply(Plan{Swap: []string{"geosite-youtube"}}, src, live, testPathFor); err == nil {
		t.Fatal("Apply must error when a Swap tag's source file is absent")
	}
}
