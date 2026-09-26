package lists

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	const digest = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5"
	if err := VerifyChecksum(path, digest); err != nil {
		t.Fatal(err)
	}
	for _, published := range []string{"  " + digest + "\n", digest + "  geosite.dat", strings.ToUpper(digest)} {
		if err := VerifyChecksum(path, published); err != nil {
			t.Fatalf("published digest %q was rejected: %v", published, err)
		}
	}
	if err := VerifyChecksum(path, "abc"); err == nil {
		t.Fatal("a truncated digest was accepted")
	}
	if err := VerifyChecksum(path, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("a wrong digest was accepted")
	}
}

func TestVerifyChecksumSkipsWhenNothingIsPublished(t *testing.T) {
	if err := VerifyChecksum("/does/not/exist", ""); err != nil {
		t.Fatalf("an unpublished digest must not require the file: %v", err)
	}
}
