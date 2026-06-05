package zapret

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/desyncgen"
)

func TestNfqwsEngineRender(t *testing.T) {
	e := NfqwsEngine{}

	// Full strategy: method + every optional knob set.
	full := desyncgen.Strategy{
		axMethod: "fake,multisplit", axSplit: "2", axSeqovl: "652",
		axTTL: "4", axFooling: "badseq", axFakeTLS: "tls_clienthello_www_google_com.bin", axRepeats: "6",
	}
	got := strings.Join(e.Render(full), " ")
	for _, want := range []string{
		"--dpi-desync=fake,multisplit", "--dpi-desync-split-pos=2", "--dpi-desync-split-seqovl=652",
		"--dpi-desync-ttl=4", "--dpi-desync-fooling=badseq", "--dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
		"--dpi-desync-repeats=6",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q in %q", want, got)
		}
	}
	if !strings.HasPrefix(got, "--dpi-desync=") {
		t.Errorf("method must come first: %q", got)
	}
}

func TestNfqwsEngineOmitsOffKnobs(t *testing.T) {
	e := NfqwsEngine{}
	// Bare method + all knobs at their off sentinel -> only the method emitted.
	s := desyncgen.Strategy{
		axMethod: "multisplit", axSeqovl: offSeqovl, axTTL: offTTL,
		axFooling: offFooling, axFakeTLS: offFakeTLS, axRepeats: offRepeats,
	}
	got := e.Render(s)
	if len(got) != 1 || got[0] != "--dpi-desync=multisplit" {
		t.Errorf("off knobs must be omitted, got %v", got)
	}
}

func TestNfqwsEngineNoMethodRendersNothing(t *testing.T) {
	if got := (NfqwsEngine{}).Render(desyncgen.Strategy{axSplit: "2"}); got != nil {
		t.Errorf("no method -> nil, got %v", got)
	}
}

// Integration: the generator core drives the nfqws engine end to end.
func TestNfqwsEngineDrivesGenerator(t *testing.T) {
	e := NfqwsEngine{}
	if got := desyncgen.Grid(e, 50); len(got) != 50 {
		t.Errorf("capped grid over nfqws axes = %d, want 50", len(got))
	}
	// Mutate a working seed: every neighbour renders to valid nfqws tokens and
	// differs from the seed's render.
	seed := desyncgen.Strategy{axMethod: "multisplit", axSplit: "2", axSeqovl: "652", axTTL: offTTL, axFooling: offFooling, axFakeTLS: offFakeTLS, axRepeats: offRepeats}
	base := strings.Join(e.Render(seed), " ")
	neigh := desyncgen.Mutate(e, seed)
	if len(neigh) == 0 {
		t.Fatal("expected neighbours")
	}
	for _, n := range neigh {
		r := e.Render(n)
		if len(r) == 0 || r[0] == "" || !strings.HasPrefix(r[0], "--dpi-desync=") {
			t.Errorf("neighbour rendered invalid: %v", r)
		}
		if strings.Join(r, " ") == base {
			t.Errorf("neighbour render identical to seed: %v", r)
		}
	}
}
