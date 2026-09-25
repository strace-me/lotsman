package lists

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRepresentationsUsesEngineLayout(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRepresentations(dir, []Representation{
		{Engine: "nfqws1", Kind: Domains, Filename: "example.hostlist", Content: "a.example\n"},
		{Engine: "sing-box", Kind: Domains, Filename: filepath.Join("rule-set", "example.json"), Content: "{}\n"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join("nfqws1", "example.hostlist"),
		filepath.Join("sing-box", "rule-set", "example.json"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestWriteRepresentationsRefusesEtc(t *testing.T) {
	err := WriteRepresentations("/etc/lotsman/generated", []Representation{
		{Engine: "nfqws1", Kind: Domains, Filename: "example.hostlist", Content: "a.example\n"},
	})
	if err == nil {
		t.Fatal("generated files were allowed into /etc")
	}
}

func TestWriteRepresentationsRejectsAbsoluteFilename(t *testing.T) {
	if err := WriteRepresentations(t.TempDir(), []Representation{
		{Engine: "nfqws1", Filename: "/etc/lotsman/rules/example.yaml"},
	}); err == nil {
		t.Fatal("absolute filename was accepted")
	}
}
