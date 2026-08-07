package zapret

import "testing"

// The capture must be derivable from a composed argv, because keeping it as a
// second literal is what left Discord voice filtering udp 50000-50100 behind a
// queue that carried only 443.
func TestCaptureFromArgsReadsEveryProfile(t *testing.T) {
	args := []string{
		"--filter-tcp=80,443", "--hostlist=yt.txt", "--dpi-desync=fake",
		"--new",
		"--filter-udp=443", "--hostlist=yt.txt", "--dpi-desync=fake",
		"--new",
		"--filter-udp=19294-19344,50000-50100", "--filter-l7=discord,stun", "--dpi-desync=fake",
	}
	c := CaptureFromArgs(args)
	if len(c.TCP) != 2 || c.TCP[0] != "443" || c.TCP[1] != "80" {
		t.Errorf("tcp = %v", c.TCP)
	}
	want := map[string]bool{"443": true, "19294-19344": true, "50000-50100": true}
	if len(c.UDP) != 3 {
		t.Fatalf("udp = %v", c.UDP)
	}
	for _, p := range c.UDP {
		if !want[p] {
			t.Errorf("unexpected udp token %q", p)
		}
	}
}

func TestUncoveredIsIntervalAware(t *testing.T) {
	have := Capture{UDP: []string{"443", "50000-50100"}}
	if u := have.Uncovered(Capture{UDP: []string{"50007"}}); len(u.UDP) != 0 {
		t.Errorf("50007 is inside 50000-50100, got uncovered %v", u.UDP)
	}
	u := have.Uncovered(Capture{UDP: []string{"19294-19344"}})
	if len(u.UDP) != 1 || u.UDP[0] != "19294-19344" {
		t.Errorf("want the voice range reported uncovered, got %v", u.UDP)
	}
}

// The scope size is what separates a targeted domain-less rule from one that
// would desync everything.
func TestPortCountSeparatesTargetedFromGlobal(t *testing.T) {
	voice := Capture{UDP: []string{"19294-19344", "50000-50100"}}
	if n := voice.PortCount(); n != 51+101 {
		t.Errorf("voice scope = %d ports, want 152", n)
	}
	global := Capture{UDP: []string{"1024-65535"}}
	if n := global.PortCount(); n < 64000 {
		t.Errorf("global scope = %d, want it to read as huge", n)
	}
}
