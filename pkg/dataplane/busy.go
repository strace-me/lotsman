package dataplane

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// realtimeKBps is the rate a flow must sustain to count as a live session. A
// voice call or a game moves tens of KiB per second continuously; the background
// chatter it must not be confused with — NTP, DNS, mDNS keepalives — moves a few
// hundred bytes in total.
const realtimeKBps = 8

// RealtimeActive reports whether the link is carrying traffic a measurement would
// disturb: a UDP flow moving real volume, which is what a game or a voice call
// looks like from here.
//
// It samples the connection table TWICE and compares byte counts, because the
// presence of a UDP flow says nothing. The first version of this asked only
// whether any UDP connection had bytes in both directions, and on a home LAN that
// is true at every instant of every day — an NTP exchange of 144 bytes each way
// satisfied it. The prospector it gated would never once have run, and would have
// reported itself as politely waiting for a quiet moment that never came.
//
// Burst probes pull real bytes to find out whether a path carries volume, and
// pulling them mid-match is exactly the wrong moment: the probe competes for the
// uplink it is measuring, so it both degrades the session and mismeasures the
// path. Deferring costs a stale measurement for one interval.
func RealtimeActive(ctx context.Context, c *ClashClient) bool {
	busy, _ := RealtimeActiveWhy(ctx, c)
	return busy
}

// RealtimeActiveWhy is RealtimeActive with the evidence behind the verdict.
//
// The reason is not decoration. Diagnosing why a gated background task never ran
// meant measuring this by hand on the box, because the verdict was a bare bool
// and "deferring" and "nothing to do" looked identical from outside. A caller
// that gates on this should log what it saw.
func RealtimeActiveWhy(ctx context.Context, c *ClashClient) (bool, string) {
	if c == nil {
		return false, "no clash api"
	}
	first, err := udpBytes(ctx, c)
	if err != nil {
		return true, "connection table unreadable: " + err.Error() // unknown is not idle
	}
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return true, "cancelled mid-sample"
	}
	second, err := udpBytes(ctx, c)
	if err != nil {
		return true, "connection table unreadable: " + err.Error()
	}
	const threshold = realtimeKBps * 1024 * 2 // KiB/s over the 2s window
	var peak int64
	for id, now := range second {
		if before, seen := first[id]; seen && now-before > peak {
			peak = now - before
		}
	}
	if peak > threshold {
		return true, fmt.Sprintf("a udp flow moved %d KiB in 2s (threshold %d KiB)", peak>>10, threshold>>10)
	}
	return false, fmt.Sprintf("quiet: busiest udp flow moved %d KiB in 2s", peak>>10)
}

// udpBytes totals each UDP flow's transferred bytes, keyed by connection id so a
// flow can be followed across the two samples.
func udpBytes(ctx context.Context, c *ClashClient) (map[string]int64, error) {
	conns, err := c.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(conns))
	for _, cn := range conns {
		if strings.EqualFold(cn.Metadata.Network, "udp") {
			out[cn.ID] = cn.Upload + cn.Download
		}
	}
	return out, nil
}
