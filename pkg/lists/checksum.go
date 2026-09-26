package lists

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// VerifyChecksum checks a downloaded source against a published sha256. It is
// only applied when the source PUBLISHES one: an absent expected digest means
// "nothing to verify", not "verified" — a checksum that is silently skipped reads
// as a checksum that passed.
func VerifyChecksum(path, expected string) error {
	if expected == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if !EqualDigest(actual, expected) {
		return fmt.Errorf("%s: sha256 is %s, source published %s", path, actual, expected)
	}
	return nil
}

// EqualDigest compares two hex digests case-insensitively and tolerates the
// "<digest>  <filename>" shape a `sha256sum` output line has, because that is
// the form an operator pastes out of a terminal.
func EqualDigest(a, b string) bool {
	return normalizeDigest(a) == normalizeDigest(b) && len(normalizeDigest(a)) == sha256.Size*2
}

// normalizeDigest trims surrounding space, lowercases, and cuts at the first
// separator AFTER the digest — a `sha256sum` line is "<hex>  <name>". Leading
// space is skipped rather than treated as the cut point, or a padded digest
// normalises to "" and every comparison with it passes or fails by accident.
func normalizeDigest(in string) string {
	out := make([]byte, 0, len(in))
	seen := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if seen {
				break
			}
			continue // leading whitespace
		}
		seen = true
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}
