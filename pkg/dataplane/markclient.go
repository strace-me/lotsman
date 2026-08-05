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
// table returns them untouched. Without the mark the probe is desynced by
// whatever the household is using, and measures the incumbent strategy while
// appearing to measure the candidate.
//
// It is deliberately DIRECT, with no proxy option. Routing the probe through
// sing-box's socks inbound would mark only the loopback socket to that port;
// the packet that actually leaves the box is sing-box's own, unmarked, and would
// miss the sandbox entirely. Since the sandbox measures desync on box egress —
// which the production nft rules already cover (`oifname <wan>`) — direct is also
// the right path, not merely the only one that works.
//
// SO_MARK needs CAP_NET_ADMIN and is Linux-only; off Linux the mark is silently
// not applied (mark_other.go), which is correct because the sandbox is Linux-only
// too. Keep-alives are off for the reason they are off everywhere here: sing-box
// routes per connection, so a pooled one reports on a route no longer in force.
func MarkedClient(mark int, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       MarkedDialer(mark, timeout).DialContext,
		},
		Timeout: timeout,
	}
}
