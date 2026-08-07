package strategycat

import "testing"

// The ID is ours; the origin is what the operator recognises. Only four of
// seventy recipes carry a preset, so an origin derived from preset alone left
// sixty-six showing a bare id — which is how the owner came to ask why the app
// would not tell him which strategy gaming-battlenet was running.
func TestOriginNamesSomethingAHumanRecognises(t *testing.T) {
	cases := []struct{ preset, provenance, want string }{
		{"ALT12", "Flowseal/zapret-discord-youtube 1.10.0, ALT12 (hostlist profiles)", "ALT12"},
		{"", "Flowseal/zapret-discord-youtube 1.9.9a", "Flowseal 1.9.9a"},
		{"", "Zapret-Manager 9.6 (zapret 72.20260307)", "Zapret-Manager 9.6"},
		{"", "Zapret-Manager 9.6 (zapret 72.20260307) | also: Flowseal/…", "Zapret-Manager 9.6"},
		{"", "", ""},
	}
	for _, c := range cases {
		got := Recipe{Preset: c.preset, Provenance: c.provenance}.Origin()
		if got != c.want {
			t.Errorf("Origin(%q,%q) = %q, want %q", c.preset, c.provenance, got, c.want)
		}
	}
}

// A few older entries put a LAN address in their provenance. Whatever else the
// compaction does, it must not put one on screen — and those entries need
// rewriting before any public release.
func TestOriginDoesNotSurfaceALanAddress(t *testing.T) {
	got := Recipe{Provenance: "router 192.168.1.1 /opt/zapret-lotsman (Flowseal 1.9.9a ported to nfqws)"}.Origin()
	if got == "" {
		t.Skip("nothing rendered, nothing leaked")
	}
	for _, bad := range []string{"192.168", "/opt/"} {
		if len(got) >= len(bad) && contains(got, bad) {
			t.Errorf("Origin surfaced %q in %q", bad, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
