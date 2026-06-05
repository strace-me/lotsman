package zapret

import "github.com/strace-me/lotsman/pkg/desyncgen"

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
	axMethod  = "method"
	axSplit   = "split_pos"
	axSeqovl  = "seqovl"
	axTTL     = "ttl"
	axFooling = "fooling"
	axFakeTLS = "fake_tls"
	axRepeats = "repeats"
)

// off sentinels: a value meaning "do not emit this knob" (lets Grid/Mutate
// explore with/without an optional technique).
const (
	offSeqovl  = "0"
	offTTL     = "0"
	offFooling = "none"
	offFakeTLS = "none"
	offRepeats = "1"
)

// Axes is the nfqws desync search space. Values are grounded in what
// blockcheck/Zapret-Manager actually use; Ordinal/Numeric order defines Mutate
// adjacency. method is always emitted; the rest are optional (off sentinel).
func (NfqwsEngine) Axes() []desyncgen.Axis {
	return []desyncgen.Axis{
		{Name: axMethod, Kind: desyncgen.Categorical, Values: []string{
			"fake", "split2", "multisplit", "fake,multisplit", "fakedsplit", "fakeddisorder", "disorder2",
		}},
		{Name: axSplit, Kind: desyncgen.Ordinal, Values: []string{"1", "2", "sniext+1", "midsld"}},
		{Name: axSeqovl, Kind: desyncgen.Numeric, Values: []string{offSeqovl, "336", "652"}},
		{Name: axTTL, Kind: desyncgen.Numeric, Values: []string{offTTL, "2", "4"}},
		{Name: axFooling, Kind: desyncgen.Categorical, Values: []string{offFooling, "badseq", "badsum", "md5sig", "ts"}},
		{Name: axFakeTLS, Kind: desyncgen.Categorical, Values: []string{offFakeTLS, "tls_clienthello_www_google_com.bin"}},
		{Name: axRepeats, Kind: desyncgen.Numeric, Values: []string{offRepeats, "2", "6"}},
	}
}

// Render turns a Strategy into nfqws desync tokens. method is required (an empty
// strategy renders nothing); each optional knob is emitted only when set to a
// non-off value. Order is stable (method first, then knobs in axis order).
func (NfqwsEngine) Render(s desyncgen.Strategy) []string {
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
	emit("--dpi-desync-repeats", s[axRepeats], offRepeats)
	return out
}
