package zapret

import "testing"

// flowseal-alt12-google, as the catalogue ships it. Its author scoped the first
// two profiles to DIFFERENT hostlists; the per-service model has one list per rule
// and puts it in every block, so the tcp/443 profile claims 443 and the
// fake+multisplit behind it can only ever see port 80 — which YouTube never uses.
// The rule ran hostfakesplit alone all day under the bundle's name, and the
// knowledge base scored the bundle for a third of itself.
func TestShadowedBlockCatchesTheGoogleBundle(t *testing.T) {
	blocks := [][]string{
		{"--filter-tcp=443", "--dpi-desync=hostfakesplit"},
		{"--filter-tcp=80,443", "--dpi-desync=fake,multisplit", "--dpi-desync-split-seqovl=664"},
		{"--filter-udp=443", "--dpi-desync=fake"},
	}
	i, why, bad := ShadowedBlock(blocks)
	if !bad {
		t.Fatal("tcp/443 followed by tcp/80,443 on the same domains is unreachable on 443")
	}
	if i != 1 {
		t.Errorf("shadowed block is %d, want 1", i)
	}
	if why == "" {
		t.Error("a refusal must name its reason")
	}
}

func TestDistinctFiltersAreNotShadowed(t *testing.T) {
	// flowseal-alt12-discord: voice UDP, then discord.media's alternate TCP ports,
	// then general TLS. Nothing overlaps, and refusing it would have cost the voice
	// call this project spent a day making work.
	discord := [][]string{
		{"--filter-udp=19294-19344,50000-50100", "--filter-l7=discord,stun", "--dpi-desync=fake"},
		{"--filter-tcp=2053,2083,2087,2096,8443", "--dpi-desync=fake,multisplit"},
		{"--filter-tcp=80,443", "--dpi-desync=fake,multisplit"},
	}
	if i, why, bad := ShadowedBlock(discord); bad {
		t.Errorf("discord bundle wrongly refused at block %d: %s", i+1, why)
	}
	// tcp and udp on the same port number are two different filters.
	if _, _, bad := ShadowedBlock([][]string{{"--filter-tcp=80,443"}, {"--filter-udp=443"}}); bad {
		t.Error("tcp/443 does not shadow udp/443")
	}
	// Same ports, but the first is narrowed by --filter-l7: the ports overlap and
	// the traffic does not.
	l7 := [][]string{
		{"--filter-udp=443", "--filter-l7=quic", "--dpi-desync=fake"},
		{"--filter-udp=443", "--dpi-desync=fake"},
	}
	if _, _, bad := ShadowedBlock(l7); bad {
		t.Error("a profile narrowed by --filter-l7 does not shadow the one behind it")
	}
}
