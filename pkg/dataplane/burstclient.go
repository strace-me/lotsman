package dataplane

import (
	"net"
	"net/http"
	"time"
)

// BurstClient returns an HTTP client that reaches the network the same way the
// probes do — through the SOCKS inbound when one is configured, directly
// otherwise — sized for pulling volume rather than fetching a status line.
//
// It exists so pkg/burstprobe can measure the path a service actually takes.
// Keep-alives are disabled for the same reason the probe client disables them:
// sing-box routes per CONNECTION, so a pooled connection keeps the outbound it
// was opened with and would report throughput for a route no longer in force.
func BurstClient(proxyAddr string, timeout time.Duration) *http.Client {
	// The two phases bounded separately, so a failure says which one it was —
	// see DialPhaseTimeout. The live canary and the lane must classify a failure
	// the same way or the two graders describe the same path differently.
	tr := &http.Transport{
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: TLSPhaseTimeout,
	}
	if proxyAddr != "" {
		d := &socks5Dialer{proxy: proxyAddr, timeout: timeout}
		tr.DialContext = d.DialContext
	} else {
		tr.DialContext = (&net.Dialer{Timeout: DialPhaseTimeout}).DialContext
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}
