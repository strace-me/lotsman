package strategystore

import "testing"

func TestParseArgs(t *testing.T) {
	parsed, err := ParseArgs([]byte("# ALT12 — converted\n\n--filter-tcp=443\n--new\n"), "", "fallback.args", false)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "ALT12" {
		t.Fatalf("name = %q, want ALT12", parsed.Name)
	}
	if len(parsed.Values) != 2 || parsed.Values[0] != "--filter-tcp=443" || parsed.Values[1] != "--new" {
		t.Fatalf("values = %#v", parsed.Values)
	}
}

func TestParseArgsDeclaredNameWinsForSingleFile(t *testing.T) {
	parsed, err := ParseArgs([]byte("# upstream\n--dpi-desync=fake\n"), "LOCAL", "fallback.args", true)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "LOCAL" {
		t.Fatalf("name = %q, want LOCAL", parsed.Name)
	}
}

func TestParseArgsRejectsEmptyFile(t *testing.T) {
	if _, err := ParseArgs([]byte("# only a comment\n"), "", "empty.args", false); err == nil {
		t.Fatal("empty args file was accepted")
	}
}
