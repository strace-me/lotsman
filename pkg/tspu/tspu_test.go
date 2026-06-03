package tspu

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/strategy"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   Signals
		want BlockType
	}{
		{"healthy", Signals{OK: true, RTTms: 30, BaselineRTT: 25}, None},
		{"throttle", Signals{OK: true, RTTms: 400, BaselineRTT: 30}, Throttle},
		{"dns poison", Signals{OK: false, ResolvedIP: "192.0.2.1"}, DNSPoison},
		{"dns poison even if ok", Signals{OK: true, ResolvedIP: "198.18.0.5", RTTms: 10}, DNSPoison},
		{"tcp reset", Signals{OK: false, Err: "read: connection reset by peer"}, TCPReset},
		{"timeout", Signals{OK: false, Err: `dial tcp 1.2.3.4:443: i/o timeout`}, Timeout},
		{"refused", Signals{OK: false, Err: "connect: connection refused"}, Refused},
		{"unknown", Signals{OK: false, Err: "some weird error"}, Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Errorf("Classify = %s, want %s", got, c.want)
			}
		})
	}
}

func TestSuggestClass(t *testing.T) {
	cases := map[BlockType]string{
		TCPReset:  strategy.ClassZapret,
		Timeout:   strategy.ClassVPN,
		Throttle:  strategy.ClassVPN,
		None:      strategy.ClassDirect,
		Refused:   "",
		DNSPoison: "",
		Unknown:   "",
	}
	for b, want := range cases {
		if got := SuggestClass(b); got != want {
			t.Errorf("SuggestClass(%s) = %q, want %q", b, got, want)
		}
	}
}
