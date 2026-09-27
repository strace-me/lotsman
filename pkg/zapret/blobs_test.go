package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobRefsParseEveryForm(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.bin")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := nfqws2Bundle(
		"--filter-tcp=443",
		"--blob=custom:@present.bin",
		"--blob=literal:0xDEADBEEF",
	)
	engine := Nfqws2Engine{}
	if err := engine.CheckBlobs(b, dir); err != nil {
		t.Fatalf("a resolvable set of blobs was refused: %v", err)
	}
	abs, err := engine.AbsolutizeBlobs(b, dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(abs, " ")
	if !strings.Contains(joined, "@"+present) {
		t.Errorf("relative blob path was not absolutised: %s", joined)
	}
	if !strings.Contains(joined, "--blob=literal:0xDEADBEEF") {
		t.Errorf("an inline hex blob was rewritten: %s", joined)
	}
}

func TestCheckBlobsRejectsAMissingFile(t *testing.T) {
	// The engine refuses this on START with "cannot access file". Catching it at
	// selection keeps a recipe that cannot load out of the candidate set.
	err := Nfqws2Engine{}.CheckBlobs(nfqws2Bundle("--blob=custom:@absent.bin"), t.TempDir())
	if err == nil {
		t.Fatal("a missing blob file was accepted")
	}
	if !strings.Contains(err.Error(), "absent.bin") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

func TestCheckBlobsRejectsAnAutoInitialisedName(t *testing.T) {
	// Verified on v0.9.5.2: the engine prints "duplicate blob name
	// 'fake_default_tls'" even when the file exists and is readable. The engine
	// already holds these contents; there is no file to find.
	dir := t.TempDir()
	path := filepath.Join(dir, "tls.bin")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Nfqws2Engine{}.CheckBlobs(nfqws2Bundle("--blob=fake_default_tls:@"+path), dir)
	if err == nil {
		t.Fatal("naming an engine-initialised blob was accepted")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error = %v", err)
	}
}

func TestCheckBlobsRejectsMalformedValues(t *testing.T) {
	for name, arg := range map[string]string{
		"no name":   "--blob=:@x.bin",
		"no value":  "--blob=custom",
		"junk":      "--blob=custom:nothex",
		"empty hex": "--blob=custom:",
	} {
		if err := (Nfqws2Engine{}).CheckBlobs(nfqws2Bundle(arg), t.TempDir()); err == nil {
			t.Errorf("%s: %q was accepted", name, arg)
		}
	}
}

func TestAbsolutizeBlobsLeavesAbsolutePathsAlone(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x.bin")
	engine := Nfqws2Engine{}
	out, err := engine.AbsolutizeBlobs(nfqws2Bundle("--blob=custom:@"+abs), "/some/other/dir")
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != "--blob=custom:@"+abs {
		t.Errorf("an absolute path was rewritten: %s", out[0])
	}
}
