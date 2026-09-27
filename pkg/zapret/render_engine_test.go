package zapret

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategy"
)

func strategyFixture(id string, args ...string) strategy.Definition {
	return strategy.Definition{ID: id, Class: strategy.ClassZapret, NFQWSArgs: args}
}

// The router launcher used to hardcode nfqws1's spelling and, worse, passed no
// fwmark at all — so on the router the engine fell back to a build default that
// the tunnel-bypass rule was never written for. The flags now come from the same
// adapter the desktop uses, so the two deployments cannot drift.
func TestLauncherFlagsComeFromTheAdapter(t *testing.T) {
	def := strategyFixture("alt12", "--dpi-desync=fake")
	out, err := RenderLauncher(def, LauncherOpts{
		FilterTCP: "443", Fwmark: DesyncFwmark, Engine: "nfqws1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--dpi-desync-fwmark=0x40000000") {
		t.Errorf("nfqws1 launcher is missing its fwmark flag:\n%s", out)
	}
	if strings.Contains(out, "--fwmark=") {
		t.Errorf("nfqws1 launcher was given nfqws2's flag spelling:\n%s", out)
	}
	// The queue stays a run-time parameter: baking it in would freeze a value the
	// executor passes per invocation.
	if !strings.Contains(out, `QNUM="${1:-200}"`) || !strings.Contains(out, `--qnum="$QNUM"`) {
		t.Errorf("the queue is no longer a run-time parameter:\n%s", out)
	}
}

func TestLauncherSpellsFlagsForTheNamedEngine(t *testing.T) {
	def := strategyFixture("alt12", "--lua-desync=fake_syndata")
	out, err := RenderLauncher(def, LauncherOpts{
		FilterTCP: "443", Fwmark: DesyncFwmark, Engine: "nfqws2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--fwmark=0x40000000") {
		t.Errorf("nfqws2 launcher is missing --fwmark:\n%s", out)
	}
	if strings.Contains(out, "--dpi-desync-fwmark") {
		t.Errorf("nfqws2 launcher was given nfqws1's flag:\n%s", out)
	}
}

func TestLauncherRejectsAnUnknownEngine(t *testing.T) {
	def := strategyFixture("alt12", "--dpi-desync=fake")
	if _, err := RenderLauncher(def, LauncherOpts{FilterTCP: "443", Engine: "nonsense"}); err == nil {
		t.Fatal("an unknown engine was accepted")
	}
}

func TestComposedLauncherCarriesTheEngineFlags(t *testing.T) {
	out, err := RenderComposed([]string{"--new=one", "--filter-tcp=443", "--dpi-desync=fake"}, "",
		LauncherOpts{Fwmark: DesyncFwmark, Engine: "nfqws1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--dpi-desync-fwmark=0x40000000") {
		t.Errorf("the composed launcher is missing its fwmark flag:\n%s", out)
	}
	if !strings.Contains(out, "--new=one") {
		t.Errorf("the composed profile was lost:\n%s", out)
	}
}

// The old default must not change: an existing router config renders byte-for-byte
// as before when no engine and no mark are named.
func TestLauncherWithoutOptionsIsUnchanged(t *testing.T) {
	def := strategyFixture("alt12", "--dpi-desync=fake")
	out, err := RenderLauncher(def, LauncherOpts{FilterTCP: "443"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `exec "$N" --qnum="$QNUM" --filter-tcp=443 --dpi-desync=fake`) {
		t.Errorf("the default rendering changed:\n%s", out)
	}
}
