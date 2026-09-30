package zapret

import "testing"

// Marks are not operator-settable, so there is nothing to range-check against a
// config. What replaces that check is asserting they do not COLLIDE — and the
// collision that matters has already cost an incident: nfqws stamps its injected
// packets with DesyncFwmark, those packets are raw and unbound, so if anything
// claims that mark the auto_route table pulls them into the tun, the DPI sees a
// duplicated split ClientHello and answers RST. Every status read stayed healthy
// while TLS timed out (CHANGELOG, measured 2026-09-22).
//
// So the bounds are enumerated at startup instead: these numbers are read from
// constants and compared, rather than trusted to stay distinct because nobody has
// changed them yet.
func TestRuntimeMarksAreDistinct(t *testing.T) {
	marks := map[string]int{
		"TuneMark (the tuner's own probe sockets, SO_MARK)":          TuneMark,
		"DesyncFwmark (nfqws stamps this on the packets it INJECTS)": DesyncFwmark,
	}
	seen := map[int]string{}
	for name, mark := range marks {
		if mark == 0 {
			t.Errorf("%s is 0, which is also \"unset\" — a mark of 0 matches everything", name)
		}
		if mark < 0 || mark > 0xFFFFFFFF {
			t.Errorf("%s = %#x is outside the 32-bit fwmark range nft accepts", name, mark)
		}
		if prev, taken := seen[mark]; taken {
			t.Errorf("%s and %s are the same mark (%#x): both would claim the same packets, "+
				"which is how a sandbox probe ends up inside the production desync path", name, prev, mark)
		}
		seen[mark] = name
	}
}

// The sandbox gets a mark through options, and 0 means "use the default". A caller
// that passes its own mark has to be told when it picked one already in use, because
// the failure is a lane measuring the wrong path and reporting it as a recipe result.
func TestSandboxMarkOfZeroFallsBackToTheSharedTuneMark(t *testing.T) {
	s := &Sandbox{Opts: IsolateOptions{}}
	if got := s.mark(); got != TuneMark {
		t.Errorf("an unset sandbox mark = %#x, want the shared TuneMark %#x", got, TuneMark)
	}
	custom := &Sandbox{Opts: IsolateOptions{Mark: 0x1234}}
	if got := custom.mark(); got != 0x1234 {
		t.Errorf("an explicit sandbox mark = %#x, want 0x1234", got)
	}
	if custom.mark() == TuneMark {
		t.Error("a sandbox was given the shared TuneMark, so it would claim the tuner's packets too")
	}
}
