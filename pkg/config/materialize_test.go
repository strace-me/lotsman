package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A source deployment small enough to reason about, valid under the real loader.
func writeSource(t *testing.T, dir string, rules map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "version: 2\n" +
		"proxy:\n  engine: sing-box\n  mode: proxy\n" +
		"desync:\n  instances:\n    nfqws1-main:\n      engine: nfqws1\n      enabled: true\n" +
		"      capture: {tcp: [443]}\n"
	if err := os.WriteFile(filepath.Join(dir, "configuration.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range rules {
		if err := os.WriteFile(filepath.Join(dir, "rules", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const okRule = "id: youtube\nname: YouTube\nenabled: true\n" +
	"match:\n  domains: [youtube.com]\n" +
	"probe:\n  type: http\n  target: https://www.youtube.com/generate_204\n" +
	"chain:\n  - tier: fallback\n    vpn: {pool: vpn_url_test}\n"

func TestDeployHashIsContentNotLocation(t *testing.T) {
	a := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	b := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	ha, err := DeployHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := DeployHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("two identical deployments in different directories hashed differently: %s vs %s", ha, hb)
	}
}

func TestDeployHashSeesRuleContentAndOrder(t *testing.T) {
	base := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	h1, err := DeployHash(base)
	if err != nil {
		t.Fatal(err)
	}
	// A rule edited: the hash must move, or drift detection is decoration.
	if err := os.WriteFile(filepath.Join(base, "rules", "youtube.yaml"), []byte(strings.Replace(okRule, "youtube.com", "youtu.be", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	h2, err := DeployHash(base)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("editing a rule did not change the hash")
	}
}

// A hash that ignored names would let renaming a rule file read as unchanged, which
// is a rename the loader treats as a delete plus an add.
func TestDeployHashSeesRuleNames(t *testing.T) {
	dir := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	h1, _ := DeployHash(dir)
	if err := os.Rename(filepath.Join(dir, "rules", "youtube.yaml"), filepath.Join(dir, "rules", "yt.yaml")); err != nil {
		t.Fatal(err)
	}
	h2, _ := DeployHash(dir)
	if h1 == h2 {
		t.Fatal("renaming a rule file did not change the hash")
	}
}

// The manifest records the hash, so hashing it would make every deployment
// disagree with itself — a self-invalidating record. A materialized target must
// therefore hash exactly like the source it came from.
func TestDeployHashIgnoresTheManifest(t *testing.T) {
	src := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	dst := filepath.Join(t.TempDir(), "etc-lotsman")
	if _, err := Materialize(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, ManifestName)); err != nil {
		t.Fatalf("no manifest was written, so this test proves nothing: %v", err)
	}

	hSource, err := DeployHash(src)
	if err != nil {
		t.Fatal(err)
	}
	hTarget, err := DeployHash(dst)
	if err != nil {
		t.Fatal(err)
	}
	if hSource != hTarget {
		t.Fatalf("a materialized target hashes differently from its source:\n source %s\n target %s", hSource, hTarget)
	}

	// And adding a manifest by hand must not move the hash either.
	plain := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	before, err := DeployHash(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, ManifestName), []byte("source_hash: deadbeef\nrules: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := DeployHash(plain)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("writing a manifest changed the deployment hash")
	}
}

func TestMaterializeCopiesAndRecordsProvenance(t *testing.T) {
	src := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	dst := filepath.Join(t.TempDir(), "etc-lotsman")

	m, err := Materialize(src, dst, "")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if m.SourceHash == "" {
		t.Fatal("manifest recorded no source hash")
	}
	if m.Rules != 1 {
		t.Fatalf("manifest says %d rules, wrote 1", m.Rules)
	}
	if _, err := os.Stat(filepath.Join(dst, "rules", "youtube.yaml")); err != nil {
		t.Fatalf("rule did not arrive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ManifestName)); err != nil {
		t.Fatalf("no manifest in the target: %v", err)
	}

	st := CheckMaterialized(src, dst)
	if st.Drifted {
		t.Fatalf("a just-materialized pair reports drift: %+v", st)
	}
	if st.AppliedHash != st.SourceHash {
		t.Fatalf("applied hash %q != source hash %q", st.AppliedHash, st.SourceHash)
	}
}

func TestMaterializeReportsDriftAfterTheSourceMoves(t *testing.T) {
	src := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	dst := filepath.Join(t.TempDir(), "etc-lotsman")
	if _, err := Materialize(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if st := CheckMaterialized(src, dst); st.Drifted {
		t.Fatal("clean pair drifted")
	}
	// The operator edits the source in the GUI. The target now describes an older
	// configuration, and nothing may call that current.
	if err := os.WriteFile(filepath.Join(src, "rules", "youtube.yaml"), []byte(strings.Replace(okRule, "youtube.com", "youtu.be", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	st := CheckMaterialized(src, dst)
	if !st.Drifted {
		t.Fatal("an edited source still reads as materialized")
	}
	if st.AppliedHash == "" {
		t.Fatal("drift reported without saying what is applied")
	}
}

// The case worth the most: materializing a broken document over a working one is a
// silent downgrade, and the failure would surface only at the next service start.
func TestMaterializeRefusesABrokenSourceAndLeavesTheTargetIntact(t *testing.T) {
	src := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	dst := filepath.Join(t.TempDir(), "etc-lotsman")
	if _, err := Materialize(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	good, err := DeployHash(dst)
	if err != nil {
		t.Fatal(err)
	}

	// A rule that does not load: the chain names a pool that does not exist.
	broken := "id: discord\nname: Discord\nenabled: true\n" +
		"match:\n  domains: [discord.com]\n" +
		"probe:\n  type: http\n  target: https://discord.com/api/v9/gateway\n" +
		"chain:\n  - tier: fallback\n    vpn: {pool: no_such_pool}\n"
	if err := os.WriteFile(filepath.Join(src, "rules", "youtube.yaml"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(src, dst, ""); err == nil {
		t.Fatal("a source that does not load was materialized")
	}

	after, err := DeployHash(dst)
	if err != nil {
		t.Fatal(err)
	}
	if after != good {
		t.Fatal("the refused materialize still changed the target")
	}
	if st := CheckMaterialized(src, dst); !st.Drifted {
		t.Fatal("a broken source reads as current against the old target")
	}
}

// A rule deleted from the source must disappear from the target, or the running
// deployment keeps steering traffic the operator asked it to stop steering.
func TestMaterializeRemovesRulesTheSourceDropped(t *testing.T) {
	src := writeSource(t, t.TempDir(), map[string]string{
		"youtube.yaml": okRule,
		"x.yaml":       strings.Replace(okRule, "youtube", "x", -1),
	})
	dst := filepath.Join(t.TempDir(), "etc-lotsman")
	if _, err := Materialize(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "rules", "x.yaml")); err != nil {
		t.Fatal("setup: x.yaml did not arrive")
	}
	if err := os.Remove(filepath.Join(src, "rules", "x.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "rules", "x.yaml")); err == nil {
		t.Fatal("a rule deleted from the source is still in the target")
	}
	if st := CheckMaterialized(src, dst); st.Drifted {
		t.Fatalf("re-materializing did not clear the drift: %+v", st)
	}
}

// A target an operator built by hand carries no manifest. Calling it current because
// the content happens to look right would be trusting an accident.
func TestCheckMaterializedFlagsAHandBuiltTarget(t *testing.T) {
	src := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	dst := writeSource(t, t.TempDir(), map[string]string{"youtube.yaml": okRule})
	if st := CheckMaterialized(src, dst); !st.Drifted {
		t.Fatal("a target with no manifest was reported as current")
	}
}

func TestCheckMaterializedReportsAMissingSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "never-written")
	dst := filepath.Join(t.TempDir(), "target")
	st := CheckMaterialized(src, dst)
	if !st.SourceMissing {
		t.Fatal("a source that does not exist was not reported missing")
	}
	if st.SourceHash != "" {
		t.Fatal("a missing source produced a hash")
	}
}

// A source with no rules at all is a legitimate first state, not a broken one.
func TestMaterializeAcceptsADocumentWithNoRules(t *testing.T) {
	src := writeSource(t, t.TempDir(), nil)
	dst := filepath.Join(t.TempDir(), "etc-lotsman")
	m, err := Materialize(src, dst, "")
	if err != nil {
		t.Fatalf("a ruleless deployment did not materialize: %v", err)
	}
	if m.Rules != 0 {
		t.Fatalf("manifest says %d rules", m.Rules)
	}
	if st := CheckMaterialized(src, dst); st.Drifted {
		t.Fatal("a ruleless pair reports drift")
	}
}
