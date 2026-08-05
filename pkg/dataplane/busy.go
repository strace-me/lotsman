package dataplane

import (
	"context"
	"strings"
)

// RealtimeActive reports whether the link is carrying traffic a measurement
// would disturb — live UDP flows, which is what a game or a voice call looks
// like from here.
//
// Burst probes pull real bytes to find out whether a path carries volume, and
// pulling them while somebody is mid-match is exactly the wrong moment: the probe
// competes for the uplink it is measuring, so it both degrades the session and
// mismeasures the path. Deferring costs a stale ranking for one interval, which
// is cheap by comparison.
//
// It reads the connection table rather than sampling bytes, so asking is free —
// one loopback API call, nothing on the wire.
func RealtimeActive(ctx context.Context, c *ClashClient) bool {
	if c == nil {
		return false
	}
	conns, err := c.Connections(ctx)
	if err != nil {
		// Unknown is not idle. A failed check must not license a burst.
		return true
	}
	for _, cn := range conns {
		if !strings.EqualFold(cn.Metadata.Network, "udp") {
			continue
		}
		// A UDP flow that has carried traffic in BOTH directions is a live
		// session; one that has only ever sent is a stalled or one-way flow and
		// says nothing about the link being busy.
		if cn.Upload > 0 && cn.Download > 0 {
			return true
		}
	}
	return false
}
