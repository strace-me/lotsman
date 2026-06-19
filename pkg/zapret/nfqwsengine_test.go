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

// The engine's vocabulary must be wide enough to express the real alt-recipes
// (docs/RESEARCH-adoptable.md), not just the narrow original 7 axes — otherwise
// the search can never rediscover a working hand recipe. Reproduce two of them.
func TestNfqwsEngineExpressesRealRecipes(t *testing.T) {
	e := NfqwsEngine{}
	cases := []struct {
		name string
		s    desyncgen.Strategy
		want []string
	}{
		{
			// ALT3 TLS: fake-SNI decoy on a whitelisted RU domain + ts fooling.
			name: "alt3-fake-sni-decoy",
			s:    desyncgen.Strategy{axMethod: "fake,hostfakesplit", axFakeTLSMod: "rnd,dupsid,sni=ya.ru", axFooling: "ts"},
			want: []string{"--dpi-desync=fake,hostfakesplit", "--dpi-desync-fake-tls-mod=rnd,dupsid,sni=ya.ru", "--dpi-desync-fooling=ts"},
		},
		{
			// QUIC/443: fake QUIC Initial, repeated.
			name: "fake-quic",
			s:    desyncgen.Strategy{axMethod: "fake", axFakeQUIC: "quic_initial_www_google_com.bin", axRepeats: "6"},
			want: []string{"--dpi-desync=fake", "--dpi-desync-fake-quic=quic_initial_www_google_com.bin", "--dpi-desync-repeats=6"},
		},
		{
			// TLS general: multisplit with seqovl=568 (a value the old axis lacked).
			name: "tls-multisplit-seqovl568",
			s:    desyncgen.Strategy{axMethod: "multisplit", axSeqovl: "568", axSplit: "1"},
			want: []string{"--dpi-desync=multisplit", "--dpi-desync-split-seqovl=568", "--dpi-desync-split-pos=1"},
		},
	}
	for _, c := range cases {
		got := e.Render(c.s)
		set := map[string]bool{}
		for _, a := range got {
			set[a] = true
		}
		for _, w := range c.want {
			if !set[w] {
				t.Errorf("%s: render missing %q in %v", c.name, w, got)
			}
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: render has extra tokens: %v (want exactly %v)", c.name, got, c.want)
		}
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
	seed := desyncgen.Strategy{axMethod: "multisplit", axSplit: "2", axSeqovl: "652", axTTL: offTTL, axFooling: offFooling, axFakeTLS: offFakeTLS, axFakeTLSMod: offFakeTLSMod, axFakeQUIC: offFakeQUIC, axRepeats: offRepeats}
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
