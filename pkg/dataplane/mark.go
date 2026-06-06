package dataplane

import (
	"net"
	"time"
)

// MarkedDialer returns a TCP/UDP dialer that stamps SO_MARK=mark on every socket
// (Linux only; a no-op elsewhere). mark==0 = unmarked (plain dialer). This is the
// box-direct, fwmark-tagged dialer the v7 tuner probes with, so its traffic can be
// diverted to an isolated test qnum without touching production.
func MarkedDialer(mark int, timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: markControl(mark)}
}
