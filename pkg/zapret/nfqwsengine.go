package zapret

import (
	"strings"

	"github.com/strace-me/lotsman/pkg/desyncgen"
)

// rawAxis carries a complete pre-baked nfqws arg string (a fixed-point raw recipe);
// when present, Render passes it through verbatim and ignores the other axes.
const rawAxis = "__raw"

// NfqwsEngine is the nfqws (zapret) profile for the desync generator (LOT-31
// v7b): it declares nfqws's DPI-desync parameter space as axes and renders a
// chosen Strategy into the `--dpi-desync*` technique tokens. It produces ONLY the
// desync technique (no --filter / --hostlist-domains) — the per-service scope is
// added at compose/apply time, exactly like a blockcheck-discovered definition's
// raw NFQWSArgs. So a generated strategy plugs straight into the existing
// composition path (scope it, then nfqwsgen.Compose).
//
// It implements desyncgen.Engine, so the shared Grid/Mutate/tester/KB machinery
// drives it unchanged; a different engine (byedpi, youtube-unblock) is just
// another implementation with its own axes + Render.
type NfqwsEngine struct{}

func (NfqwsEngine) Name() string { return "nfqws" }

// Axis names (kept as constants so the engine and any tuner agree on the keys).
const (
	axMethod     = "method"
	axSplit      = "split_pos"
	axSeqovl     = "seqovl"
	axTTL        = "ttl"
	axFooling    = "fooling"
	axFakeTLS    = "fake_tls"
	axFakeTLSMod = "fake_tls_mod"
	axFakeQUIC   = "fake_quic"
	axFakedMod   = "faked_mod" // --dpi-desync-fakedsplit-mod (only bites on faked* methods)
	axIPFrag     = "ipfrag2"   // --dpi-desync-ipfrag2: L3 IP-fragment split, orthogonal to L4/L7 splits
	axRepeats    = "repeats"
)

// off sentinels: a value meaning "do not emit this knob" (lets Grid/Mutate
// explore with/without an optional technique).
const (
	offSeqovl     = "0"
	offTTL        = "0"
	offFooling    = "none"
	offFakeTLS    = "none"
	offFakeTLSMod = "none"
	offFakeQUIC   = "none"
	offFakedMod   = "none"
	offIPFrag     = "0"
	offRepeats    = "1"
)

// Axes is the nfqws desync search space. Values are grounded in what
// blockcheck/Zapret-Manager and the live alt-recipes actually use (docs/
// RESEARCH-adoptable.md); Ordinal/Numeric order defines Mutate adjacency. method
// is always emitted; the rest are optional (off sentinel). fake_tls_mod and
// fake_quic only bite when the method fakes — nfqws ignores them otherwise, so a
// nonsensical combo just wastes a probe; the fitness ranks it out.
func (NfqwsEngine) Axes() []desyncgen.Axis {
	return []desyncgen.Axis{
		{Name: axMethod, Kind: desyncgen.Categorical, Values: []string{
			"fake", "split2", "multisplit", "multidisorder", "fake,multisplit", "fakedsplit", "fakeddisorder", "disorder2",
			"fake,hostfakesplit", "syndata",
		}},
		// split-pos is a LIST of marker+offset tokens (not a scalar): method/host/
		// sld/midsld/sniext each with ±N, comma-chained for multi-cut. multisplit/
		// multidisorder consume the whole list; faked* honor only the first.
		{Name: axSplit, Kind: desyncgen.Categorical, Values: []string{
			"1", "method+2", "sniext+1", "midsld", "host+1", "method+2,midsld", "sniext+1,midsld",
		}},
		{Name: axSeqovl, Kind: desyncgen.Numeric, Values: []string{offSeqovl, "336", "568", "652"}},
		{Name: axTTL, Kind: desyncgen.Numeric, Values: []string{offTTL, "2", "4"}},
		{Name: axFooling, Kind: desyncgen.Categorical, Values: []string{offFooling, "badseq", "badsum", "md5sig", "ts"}},
		{Name: axFakeTLS, Kind: desyncgen.Categorical, Values: []string{offFakeTLS, "tls_clienthello_www_google_com.bin"}},
		// fake-tls-mod: the SNI-decoy + per-packet randomization trick. `rnd` re-
		// randomizes the fake ClientHello every packet so DPI can't MEMORIZE a static
		// fake (the documented cause of strategies decaying over weeks); `sni=` points
		// the decoy at a whitelisted domestic name so if DPI acts on it, it acts on
		// something harmless. The randomizing variants are the seed default (#2).
		{Name: axFakeTLSMod, Kind: desyncgen.Categorical, Values: []string{offFakeTLSMod, "rnd,dupsid", "rnd,dupsid,sni=ya.ru", "rnd,dupsid,sni=vk.com", "rnd,dupsid,sni=max.ru"}},
		{Name: axFakeQUIC, Kind: desyncgen.Categorical, Values: []string{offFakeQUIC, "quic_initial_www_google_com.bin"}},
		// fakedsplit-mod=altorder reorders the faked split segments (faked* methods only).
		{Name: axFakedMod, Kind: desyncgen.Categorical, Values: []string{offFakedMod, "altorder=1"}},
		// IP-layer fragmentation (L3) — beats DPI that reassembles TCP but not IP frags.
		{Name: axIPFrag, Kind: desyncgen.Numeric, Values: []string{offIPFrag, "24", "32"}},
		{Name: axRepeats, Kind: desyncgen.Numeric, Values: []string{offRepeats, "2", "6"}},
	}
}

// seed builds a full strategy: every optional axis at its off sentinel, then the
// caller's overrides. A complete seed means Mutate steps AWAY from off (never back
// to it), so no neighbour collapses to a render-identical no-op.
func seed(over desyncgen.Strategy) desyncgen.Strategy {
	s := desyncgen.Strategy{
		axSplit: "1", axSeqovl: offSeqovl, axTTL: offTTL, axFooling: offFooling,
		axFakeTLS: offFakeTLS, axFakeTLSMod: offFakeTLSMod, axFakeQUIC: offFakeQUIC,
		axFakedMod: offFakedMod, axIPFrag: offIPFrag, axRepeats: offRepeats,
	}
	for k, v := range over {
		s[k] = v
	}
	return s
}

// Seeds is the prior catalog: hand-proven recipes decomposed into axes (the alt-
// recipes in docs/RESEARCH-adoptable.md + the SlenderSolo/zapret-manager
// strategies.txt enumeration). A cold search starts from these instead of a blind
// Grid — priors are the strongest lever on a near-binary fitness landscape. The
// RANDOMIZING fake variants lead (they outlast static fakes DPI memorizes). The
// exact live alt12 (multi-stage --new/ipset) isn't a single-profile point and is
// added once captured from the router.
func (NfqwsEngine) Seeds() []desyncgen.Strategy {
	return []desyncgen.Strategy{
		// LEAD: per-packet-randomized fake-SNI decoy (the anti-memorization primitive
		// both Flowseal and the signature research converged on) — domestic decoy SNI.
		seed(desyncgen.Strategy{axMethod: "fake,hostfakesplit", axFakeTLSMod: "rnd,dupsid,sni=ya.ru", axFooling: "ts"}),
		seed(desyncgen.Strategy{axMethod: "fake,hostfakesplit", axFakeTLSMod: "rnd,dupsid,sni=vk.com", axFooling: "ts"}),
		// SlenderSolo TLS: multidisorder/multisplit with multi-cut split positions.
		seed(desyncgen.Strategy{axMethod: "multidisorder", axSplit: "method+2,midsld"}),
		seed(desyncgen.Strategy{axMethod: "multisplit", axSplit: "method+2,midsld"}),
		// SlenderSolo fakedsplit: short-TTL fake interleave + altorder.
		seed(desyncgen.Strategy{axMethod: "fakedsplit", axSplit: "midsld", axTTL: "2", axFakedMod: "altorder=1"}),
		// fake with short TTL + badseq fooling (the fake-ttl family).
		seed(desyncgen.Strategy{axMethod: "fake", axTTL: "2", axFooling: "badseq"}),
		// TLS general: multisplit + seqovl (push real SNI past the reassembly window).
		seed(desyncgen.Strategy{axMethod: "multisplit", axSeqovl: "568", axSplit: "1"}),
		// QUIC/443: fake QUIC Initial, repeated.
		seed(desyncgen.Strategy{axMethod: "fake", axFakeQUIC: "quic_initial_www_google_com.bin", axRepeats: "6"}),
	}
}

// rawRecipes is a curated set of proven, fixed-point nfqws desync techniques —
// the technique portion only (no --filter/--hostlist, added at compose time). They
// cover the families a battle-tested community library enumerates (credit:
// SlenderSolo/zapret-manager strategies.txt + bol-van/zapret docs) and exist as
// cold-start priors the search tests as-is and explores around. NOT vendored
// wholesale — a representative spread of public flag combinations.
var rawRecipes = []string{
	// multisplit / multidisorder with single + multi-cut split positions
	"--dpi-desync=multisplit --dpi-desync-split-pos=method+2",
	"--dpi-desync=multisplit --dpi-desync-split-pos=midsld",
	"--dpi-desync=multisplit --dpi-desync-split-pos=method+2,midsld",
	"--dpi-desync=multisplit --dpi-desync-split-pos=1,midsld,sniext+1",
	"--dpi-desync=multidisorder --dpi-desync-split-pos=method+2",
	"--dpi-desync=multidisorder --dpi-desync-split-pos=midsld",
	"--dpi-desync=multidisorder --dpi-desync-split-pos=method+2,midsld",
	// multisplit with sequence-overlap (push real SNI past the reassembly window)
	"--dpi-desync=multisplit --dpi-desync-split-seqovl=652 --dpi-desync-split-pos=1",
	"--dpi-desync=multisplit --dpi-desync-split-seqovl=336 --dpi-desync-split-pos=1",
	// fake with short TTL / fooling variants
	"--dpi-desync=fake --dpi-desync-ttl=1",
	"--dpi-desync=fake --dpi-desync-ttl=5",
	"--dpi-desync=fake --dpi-desync-fooling=badseq",
	"--dpi-desync=fake --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2",
	"--dpi-desync=fake --dpi-desync-fooling=datanoack",
	"--dpi-desync=fake --dpi-desync-fooling=ts",
	"--dpi-desync=fake --dpi-desync-fooling=md5sig",
	// fakedsplit (single-position faked interleave) + altorder
	"--dpi-desync=fakedsplit --dpi-desync-ttl=1 --dpi-desync-split-pos=method+2",
	"--dpi-desync=fakedsplit --dpi-desync-ttl=1 --dpi-desync-split-pos=midsld",
	"--dpi-desync=fakedsplit --dpi-desync-ttl=1 --dpi-desync-split-pos=midsld --dpi-desync-fakedsplit-mod=altorder=1",
	"--dpi-desync=fakedsplit --dpi-desync-fooling=badseq --dpi-desync-split-pos=midsld",
	"--dpi-desync=fakeddisorder --dpi-desync-ttl=1 --dpi-desync-split-pos=midsld",
	// TLS: randomized fake ClientHello with a domestic decoy SNI (anti-memorization)
	"--dpi-desync=fake,multisplit --dpi-desync-fake-tls-mod=rnd,dupsid,sni=ya.ru --dpi-desync-split-pos=1 --dpi-desync-fooling=ts",
	"--dpi-desync=fake,hostfakesplit --dpi-desync-fake-tls-mod=rnd,dupsid,sni=vk.com --dpi-desync-fooling=ts",
	// QUIC/443: fake QUIC Initial, repeated
	"--dpi-desync=fake --dpi-desync-fake-quic=quic_initial_www_google_com.bin --dpi-desync-repeats=6",
	"--dpi-desync=fake --dpi-desync-fake-quic=quic_initial_www_google_com.bin --dpi-desync-repeats=11",
}

// RawSeeds returns the fixed-point recipe catalog as raw strategies (tested as-is,
// never mutated). Implements desyncgen.RawSeeder.
func (NfqwsEngine) RawSeeds() []desyncgen.Strategy {
	out := make([]desyncgen.Strategy, len(rawRecipes))
	for i, r := range rawRecipes {
		out[i] = desyncgen.Strategy{rawAxis: r}
	}
	return out
}

// Render turns a Strategy into nfqws desync tokens. method is required (an empty
// strategy renders nothing); each optional knob is emitted only when set to a
// non-off value. Order is stable (method first, then knobs in axis order).
func (NfqwsEngine) Render(s desyncgen.Strategy) []string {
	if raw := s[rawAxis]; raw != "" {
		return strings.Fields(raw) // fixed-point recipe: pass through verbatim
	}
	m := s[axMethod]
	if m == "" {
		return nil // no method -> not a valid desync strategy
	}
	out := []string{"--dpi-desync=" + m}
	emit := func(flag, val, off string) {
		if val != "" && val != off {
			out = append(out, flag+"="+val)
		}
	}
	emit("--dpi-desync-split-pos", s[axSplit], "")
	emit("--dpi-desync-split-seqovl", s[axSeqovl], offSeqovl)
	emit("--dpi-desync-ttl", s[axTTL], offTTL)
	emit("--dpi-desync-fooling", s[axFooling], offFooling)
	emit("--dpi-desync-fake-tls", s[axFakeTLS], offFakeTLS)
	emit("--dpi-desync-fake-tls-mod", s[axFakeTLSMod], offFakeTLSMod)
	emit("--dpi-desync-fake-quic", s[axFakeQUIC], offFakeQUIC)
	emit("--dpi-desync-fakedsplit-mod", s[axFakedMod], offFakedMod)
	emit("--dpi-desync-ipfrag2", s[axIPFrag], offIPFrag)
	emit("--dpi-desync-repeats", s[axRepeats], offRepeats)
	return out
}
