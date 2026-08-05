package dataplane

import (
	"net/http"
	"time"
)

// MarkedClient returns an HTTP client whose sockets carry fwmark, so nft can tell
// its packets apart from everyone else's.
//
// This is what makes a desync sandbox a sandbox: the mark is the only thing
// distinguishing a measurement from real traffic, and the two nft tables key on
// it — the sandbox queues marked packets to the candidate engine, the production
// table returns them untouched. Without the mark the probe would be desynced by
// whatever the household is using and would measure the incumbent strategy.
//
// SO_MARK needs CAP_NET_ADMIN and is Linux-only; off Linux the mark is silently
// not applied (see mark_other.go), which is correct because the sandbox itself is
// Linux-only too. Keep-alives are off for the reason they are off everywhere
// here: sing-box routes per connection, so a pooled one reports on a route that
// is no longer in force.
func MarkedClient(mark int, proxyAddr string, timeout time.Duration) *http.Client {
	tr := &http.Transport{DisableKeepAlives: true}
	if proxyAddr != "" {
		// Through the tunnel's socks inbound, still marked: the mark applies to
		// the socket carrying the tunnelled bytes, which is the one nft sees.
		d := &socks5Dialer{proxy: proxyAddr, timeout: timeout}
		tr.DialContext = d.DialContext
	} else {
		tr.DialContext = MarkedDialer(mark, timeout).DialContext
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}
