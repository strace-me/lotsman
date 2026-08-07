package nfqwsgen

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// Attaching an upstream exclude list put 112 domains into argv for EVERY profile
// of the block — about a kilobyte each, repeated, in the process table and in
// every argv the engine records when it exits. A file costs one path, and nfqws
// re-reads it on mtime, so changing the list no longer restarts the engine and
// drops the desync on every live connection.
func TestExcludePathIsUsedInsteadOfInliningEveryDomain(t *testing.T) {
	many := make([]string, 0, 112)
	for i := 0; i < 112; i++ {
		many = append(many, "excluded-"+strings.Repeat("x", 8)+".example")
	}
	b := Block{
		Service:      "youtube",
		Domains:      []string{"youtube.com"},
		HostlistPath: "/lists/youtube.txt",
		Exclude:      many,
		ExcludePath:  "/lists/youtube-exclude.txt",
		Recipe: strategycat.Recipe{
			ID:        "r",
			NfqwsArgs: []string{"--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"},
		},
	}
	argv := strings.Join(Compose([]Block{b}), " ")
	if !strings.Contains(argv, "--hostlist-exclude=/lists/youtube-exclude.txt") {
		t.Fatalf("exclusions were not referenced by file: %s", argv)
	}
	if strings.Contains(argv, "--hostlist-exclude-domains=") {
		t.Error("exclusions were inlined as well as filed")
	}
	if len(argv) > 400 {
		t.Errorf("argv is %d bytes; the point of the file is that 112 domains do not land in it", len(argv))
	}
}

// Without a path the inline form stays: a caller with no hostlist dir (the router's
// legacy path) must keep working exactly as before.
func TestExcludeStillInlinesWithoutAPath(t *testing.T) {
	b := Block{
		Service: "x", Domains: []string{"x.com"}, Exclude: []string{"cdn.example"},
		Recipe: strategycat.Recipe{ID: "r", NfqwsArgs: []string{"--filter-tcp=443", "--hostlist-domains={{DOMAINS}}"}},
	}
	argv := strings.Join(Compose([]Block{b}), " ")
	if !strings.Contains(argv, "--hostlist-exclude-domains=cdn.example") {
		t.Errorf("inline form lost: %s", argv)
	}
}
