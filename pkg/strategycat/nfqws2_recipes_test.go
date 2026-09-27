package strategycat

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Every shipped nfqws2 recipe is checked against the engine it claims to target.
//
// This is the only automated gate there is. nfqws2 does NOT resolve a desync
// function name at startup — verified on v0.9.5.2, where both a real function and
// a typo pass --dry-run — and it does not check that a blob name or a split marker
// exists. So `--dry-run` proves the ARGUMENTS are ones the engine accepts, which is
// strictly more than nothing and strictly less than "this strategy desyncs". The
// second half is the canary's job on real traffic.
//
// Skipped when nfqws2 is not on the box, so a laptop without it is not a failure.
func TestShippedNfqws2RecipesPassTheRealEngine(t *testing.T) {
	bin := nfqws2Binary()
	if bin == "" {
		t.Skip("nfqws2 not on this box")
	}
	found := 0
	for _, r := range Load() {
		if r.EngineOf() != "nfqws2" {
			continue
		}
		found++
		args := []string{"--dry-run", "--qnum=249"}
		for _, a := range r.AllArgs() {
			args = append(args, strings.ReplaceAll(a, "{{DOMAINS}}", "example.com"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		cancel()
		if err != nil {
			t.Errorf("recipe %q (zapret2 >= %s) is refused by the installed engine: %v\n%s\nargs: %s",
				r.ID, r.MinEngine, err, out, strings.Join(args, " "))
		}
	}
	if found == 0 {
		t.Fatal("no nfqws2 recipes are shipped")
	}
}

func nfqws2Binary() string {
	for _, candidate := range []string{"/run/current-system/sw/bin/nfqws2", "/usr/bin/nfqws2"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// A recipe that names an engine it does not belong to is the failure that silently
// disables desync: nfqws1 does not know --lua-desync, nfqws2 does not know
// --dpi-desync, and neither reports it as an error.
func TestNfqws2RecipesDoNotCarryNfqws1Flags(t *testing.T) {
	for _, r := range Load() {
		if r.EngineOf() != "nfqws2" {
			continue
		}
		joined := strings.Join(r.AllArgs(), " ")
		for _, foreign := range []string{"--dpi-desync=", "--dpi-desync-fake", "--dpi-desync-fooling"} {
			if strings.Contains(joined, foreign) {
				t.Errorf("nfqws2 recipe %q carries an nfqws1 flag %q", r.ID, foreign)
			}
		}
		if !strings.Contains(joined, "--lua-desync=") {
			t.Errorf("nfqws2 recipe %q has no desync action at all: %s", r.ID, joined)
		}
	}
}
