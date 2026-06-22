package dataplane

import (
	"math"
	"testing"
)

// Real `ss -ti state established` output from the R5S (no State column; local
// addr carries a %eth0 zone; named port :https; retrans token present on one,
// absent on another).
const ssSample = `Recv-Q Send-Q     Local Address:Port          Peer Address:Port
0      0      192.0.2.10%eth0:53138        198.51.100.20:8443
	 cubic wscale:7,7 rto:210 rtt:23.5/11.2 ato:40 mss:1448 data_segs_out:200 retrans:0/6 rcv_space:14480
0      0         192.0.2.11:https        192.168.1.141:41580
	 cubic wscale:10,7 rto:220 rtt:10.449/3.875 mss:1448 data_segs_out:8 minrtt:3.497 rcv_wnd:73088
`

func TestParseSS(t *testing.T) {
	conns := ParseSS(ssSample)
	if len(conns) != 2 {
		t.Fatalf("got %d conns, want 2", len(conns))
	}

	a := conns[0]
	if a.Peer != "198.51.100.20:8443" { // last addr token, despite no State column
		t.Errorf("peer = %q, want 198.51.100.20:8443", a.Peer)
	}
	if math.Abs(a.RTTms-23.5) > 0.01 || math.Abs(a.RTTVarMs-11.2) > 0.01 {
		t.Errorf("rtt = %.1f/%.1f, want 23.5/11.2", a.RTTms, a.RTTVarMs)
	}
	if a.RetransTot != 6 || a.DataSegsOut != 200 {
		t.Errorf("retrans/segs = %d/%d, want 6/200", a.RetransTot, a.DataSegsOut)
	}
	if math.Abs(a.LossFraction()-6.0/200.0) > 1e-9 {
		t.Errorf("loss = %.4f, want %.4f", a.LossFraction(), 6.0/200.0)
	}

	// Second conn: named port peer, no retrans token (=> 0).
	if conns[1].Peer != "192.168.1.141:41580" || conns[1].RetransTot != 0 {
		t.Errorf("conn2 = %+v", conns[1])
	}
	if math.Abs(conns[1].RTTms-10.449) > 0.01 {
		t.Errorf("conn2 rtt = %.3f, want 10.449", conns[1].RTTms)
	}
}

func TestParseSSEmpty(t *testing.T) {
	if got := ParseSS(""); len(got) != 0 {
		t.Errorf("empty -> %v", got)
	}
}
