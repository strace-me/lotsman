package main

import (
	"os/exec"
	"strings"
	"testing"
)

// "Which build is on the box?" had no answer that did not require the daemon to
// be running: on the router it meant `logread | grep "lotsmand starting"`, which
// fails exactly when the question matters — after a crash loop, or before a
// start. pkg/version was written to make that answerable and only had half of
// what it needed.
func TestVersionFlagAnswersWithoutStarting(t *testing.T) {
	out, err := exec.Command("go", "run", ".", "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("-version did not exit cleanly: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got == "" {
		t.Fatal("-version printed nothing")
	}
	// It must not have started: no config was given, and a start would have logged
	// its way through subscriptions and pools before failing.
	if strings.Contains(got, "lotsmand starting") || strings.Contains(got, "level=") {
		t.Errorf("-version started the daemon instead of answering: %q", got)
	}
}
