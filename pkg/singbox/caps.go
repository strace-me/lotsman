package singbox

import (
	"strconv"
	"strings"
)

// Feature names a version-gated capability the generator may emit. Before
// emitting a knob whose availability depends on the sing-box version, the
// generator asks Caps.Supports — so the SAME config YAML degrades gracefully on
// an older (or future) sing-box: an unsupported knob is skipped and reported,
// never emitted as an invalid field that would fail `sing-box check`.
//
// This is the multi-VERSION seam. The multi-CORE seam (xray/mihomo) extracts
// Feature + a Core interface into pkg/core once a second core actually exists —
// not before (no speculative abstraction for a single core).
type Feature string

const (
	FeatureTLSFragment Feature = "tls_fragment" // route rule-action TLS ClientHello fragmentation
	FeatureUTLS        Feature = "utls"         // tls.utls fingerprint mimicry
	FeatureAnyTLS      Feature = "anytls"       // anytls outbound
	FeatureFakeIP      Feature = "fakeip"       // dns fakeip
	FeatureGeckoObfs   Feature = "obfs_gecko"   // hysteria2 gecko obfs — REFUTED/fabricated, never emit
	FeatureMultiplex   Feature = "multiplex"    // outbound multiplex (smux/yamux/h2mux) + padding + brutal
	FeatureShadowTLS   Feature = "shadowtls"    // shadowtls v3 outbound (ss detours through it)
	FeatureWireGuard   Feature = "wireguard"    // wireguard endpoint (the deprecated outbound form is not emitted)
	FeatureECH         Feature = "ech"          // tls.ech base form (enabled + explicit config); not the 1.13+ query_server_name
)

// baselineVersion is the R5S box; an empty/unknown target resolves to it.
const baselineVersion = "1.12.17"

// minVersion is the first sing-box version that supports each gated feature.
//
// SOURCING: VERIFIED on the real R5S (sing-box 1.12.17) via `sing-box check`
// and the real upstream doc (raw github, branch "testing"), 01.06.2026 — NOT the
// fabricated offline mirror:
//   - tls_fragment + tls_record_fragment + tls_fragment_fallback_delay: ACCEPTED
//   - anytls outbound: ACCEPTED ; tls.utls.fingerprint: ACCEPTED
//   - hy2 obfs "gecko": REJECTED on 1.12.17 ("unknown obfs type"); the real doc
//     shows gecko is genuine but "Since sing-box 1.14.0" (with min/max_packet_size).
//   - fakeip: BOTH legacy (deprecated 1.12.0) and new `type:"fakeip"` exist in 1.12.17
//   - tls.ech.fragment / ech.query_server_name: REJECTED ("unknown field") — NOT in 1.12.17 (1.13+)
//
// utls is long-standing (safe low bound). FakeIP/ECH left ungated until we emit them.
var minVersion = map[Feature]string{
	FeatureTLSFragment: "1.12.0",
	FeatureUTLS:        "1.5.0",
	FeatureAnyTLS:      "1.12.0",
	FeatureGeckoObfs:   "1.14.0", // real, added 1.14.0 (verified vs real doc); 1.12.17 rejects it
	FeatureFakeIP:      "1.12.0", // type:"fakeip" DNS server (new form) — verified accepted on 1.12.17
	FeatureMultiplex:   "1.1.0",  // smux/yamux long-standing; padding (1.3+) & brutal (1.7+) ≤ 1.12.17 baseline
	FeatureShadowTLS:   "1.3.0",  // shadowtls v3 — verified accepted on 1.12.17
	FeatureWireGuard:   "1.11.0", // `endpoints` form (outbound form deprecated on 1.12.17)
	FeatureECH:         "1.8.0",  // base ech (enabled + config) — verified accepted on 1.12.17; query_server_name is 1.13+ and never emitted
}

// Caps reports which features a target sing-box version supports.
type Caps struct{ version string }

// Capabilities resolves the capability set for a sing-box version (e.g.
// "1.12.17"). Empty/garbled -> the project baseline.
func Capabilities(version string) Caps {
	if strings.TrimSpace(version) == "" {
		version = baselineVersion
	}
	return Caps{version: version}
}

// Version returns the target version string the caps were built for.
func (c Caps) Version() string { return c.version }

// Supports reports whether the target version supports f. A feature with no
// table entry defaults to supported — we only gate knobs we have a real version
// boundary for, so an ungated knob is not silently blocked.
func (c Caps) Supports(f Feature) bool {
	min, ok := minVersion[f]
	if !ok {
		return true
	}
	return atLeast(c.version, min)
}

// atLeast reports a >= b for dotted numeric versions ("1.12.17" >= "1.12.0").
// A leading "v" and any suffix (e.g. "-alpha.3") are ignored; missing parts = 0.
func atLeast(a, b string) bool {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return true
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		num := part
		for j, r := range part {
			if r < '0' || r > '9' {
				num = part[:j] // strip suffix like "17-alpha"
				break
			}
		}
		n, _ := strconv.Atoi(num)
		out[i] = n
	}
	return out
}
