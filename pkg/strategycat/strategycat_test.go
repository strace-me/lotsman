package strategycat

import (
	"strings"
	"testing"
)

func TestLoadNonEmpty(t *testing.T) {
	got := Load()
	if len(got) < 50 {
		t.Fatalf("expected a substantial catalog, got %d recipes", len(got))
	}
}

func TestRecipesWellFormed(t *testing.T) {
	validClass := map[TargetClass]bool{
		ClassDiscordTCP: true, ClassQUIC: true, ClassYouTube: true,
		ClassGeneralTLS: true, ClassGames: true, ClassOther: true,
	}
	validProto := map[Protocol]bool{ProtoTCP: true, ProtoUDP: true, ProtoQUIC: true}

	seenID := map[string]bool{}
	seenArgs := map[string]bool{}
	for _, r := range Load() {
		if r.ID == "" {
			t.Errorf("recipe with empty id: %+v", r)
		}
		if seenID[r.ID] {
			t.Errorf("duplicate recipe id %q", r.ID)
		}
		seenID[r.ID] = true

		if !validClass[r.TargetClass] {
			t.Errorf("%s: invalid target_class %q", r.ID, r.TargetClass)
		}
		if !validProto[r.Protocol] {
			t.Errorf("%s: invalid protocol %q", r.ID, r.Protocol)
		}
		if r.Provenance == "" {
			t.Errorf("%s: missing provenance", r.ID)
		}
		if len(r.AllBlocks()) == 0 {
			t.Errorf("%s: empty nfqws_args", r.ID)
		}

		// every recipe is exactly one --new block: it must NOT contain --new,
		// and must carry a filter selecting its traffic.
		hasFilter := false
		for _, a := range r.AllArgs() {
			if a == "--new" {
				t.Errorf("%s: nfqws_args contains --new (a recipe is a single block)", r.ID)
			}
			if strings.HasPrefix(a, "--filter-") {
				hasFilter = true
			}
		}
		if !hasFilter {
			t.Errorf("%s: no --filter-* arg", r.ID)
		}

		// args must be normalized: no absolute upstream paths leaked in.
		joined := strings.Join(r.AllArgs(), " ")
		for _, bad := range []string{"/opt/zapret", "%BIN%", "%LISTS%", "$B/", "$L/"} {
			if strings.Contains(joined, bad) {
				t.Errorf("%s: un-normalized path token %q in args", r.ID, bad)
			}
		}

		// dedup invariant: no two recipes share an identical arg list.
		key := joined
		if seenArgs[key] {
			t.Errorf("%s: duplicate nfqws_args not deduped", r.ID)
		}
		seenArgs[key] = true
	}
}

func TestDeriveTagsConsistentWithArgs(t *testing.T) {
	for _, r := range Load() {
		joined := strings.Join(r.AllArgs(), " ")
		for _, tag := range r.Techniques {
			switch {
			case tag == "seqovl":
				if !strings.Contains(joined, "--dpi-desync-split-seqovl=") {
					t.Errorf("%s: tag seqovl but no split-seqovl arg", r.ID)
				}
			case tag == "fake-quic":
				if !strings.Contains(joined, "--dpi-desync-fake-quic=") {
					t.Errorf("%s: tag fake-quic but no fake-quic arg", r.ID)
				}
			case strings.HasPrefix(tag, "fooling:"):
				if !strings.Contains(joined, "--dpi-desync-fooling=") {
					t.Errorf("%s: tag %q but no fooling arg", r.ID, tag)
				}
			case strings.HasPrefix(tag, "repeats:"):
				if !strings.Contains(joined, "--dpi-desync-repeats=") {
					t.Errorf("%s: tag %q but no repeats arg", r.ID, tag)
				}
			}
		}
	}
}

func TestByClass(t *testing.T) {
	disc := ByClass(ClassDiscordTCP)
	if len(disc) == 0 {
		t.Fatal("expected discord_tcp recipes")
	}
	for _, r := range disc {
		if r.TargetClass != ClassDiscordTCP {
			t.Errorf("ByClass returned %s with class %q", r.ID, r.TargetClass)
		}
	}
	if got := len(ByClass("nonexistent")); got != 0 {
		t.Errorf("ByClass(nonexistent) = %d, want 0", got)
	}
}

func TestByProtocol(t *testing.T) {
	tcp := ByProtocol(ProtoTCP)
	udp := ByProtocol(ProtoUDP)
	if len(tcp) == 0 || len(udp) == 0 {
		t.Fatalf("expected both tcp and udp recipes, got tcp=%d udp=%d", len(tcp), len(udp))
	}
	for _, r := range tcp {
		if r.Protocol != ProtoTCP {
			t.Errorf("ByProtocol(tcp) returned %s with proto %q", r.ID, r.Protocol)
		}
	}
}
