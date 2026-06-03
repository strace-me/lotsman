package ttl

import "testing"

func TestGuessInitialTTL(t *testing.T) {
	cases := map[int]int{
		58:  64,  // Linux start 64, 6 hops
		120: 128, // Windows start 128
		250: 255, // network gear start 255
		64:  64,
		65:  128,
	}
	for observed, want := range cases {
		if got := GuessInitialTTL(observed); got != want {
			t.Errorf("GuessInitialTTL(%d) = %d, want %d", observed, got, want)
		}
	}
}

func TestHopsAway(t *testing.T) {
	// An injected RST arriving with TTL 58 (from a Linux-stack DPI 64 start)
	// is 6 hops away.
	if got := HopsAway(58); got != 6 {
		t.Errorf("HopsAway(58) = %d, want 6", got)
	}
	if got := HopsAway(122); got != 6 { // 128-122
		t.Errorf("HopsAway(122) = %d, want 6", got)
	}
	if got := HopsAway(0); got != 0 {
		t.Errorf("HopsAway(0) = %d, want 0", got)
	}
}

func TestRecommendDesyncTTL(t *testing.T) {
	if got := RecommendDesyncTTL(6); got != 6 {
		t.Errorf("= %d, want 6", got)
	}
	if got := RecommendDesyncTTL(0); got != 1 { // floor
		t.Errorf("= %d, want floor 1", got)
	}
}

func TestParseTraceroute(t *testing.T) {
	out := `traceroute to discord.com (162.159.128.233), 30 hops max
 1  192.168.1.1  0.5 ms  0.4 ms  0.4 ms
 2  100.64.0.1  3.2 ms  3.1 ms  3.0 ms
 3  * * *
 4  10.0.0.1  5.0 ms  4.9 ms  5.1 ms
`
	if got := ParseTraceroute(out); got != 3 { // hops 1,2,4 reached; 3 is timeout
		t.Errorf("ParseTraceroute = %d, want 3", got)
	}
}
