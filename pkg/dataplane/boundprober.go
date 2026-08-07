package dataplane

import (
	"net"
	"net/http"
	"time"
)

// NewMultiProberBound builds a prober whose HTTP and TCP probes leave on the
// physical interface directly, bypassing the routing table — and therefore our
// own tun.
//
// It is the answer to the one configuration LOT-44 could not serve. Probing an
// INACTIVE direct/zapret rung means asking "would traffic work if it went out
// unprotected but desynced", and in tun mode a plain dial cannot ask that: the
// tun captures it and sing-box routes it by the service's rule, into whatever
// VPN node the rule currently sits on. The prober then credits the desync rung
// with the tunnel's health, which is how a rule silently recovers onto a path
// nobody measured. Refusing to answer (Unmeasured) was the honest stopgap; this
// answers properly.
//
// The packet still meets nfqws: the production nft rule keys on `oifname <wan>`,
// which is the interface we bind to. So the measurement covers the desync the
// rung would actually get, not a bare direct path.
//
// iface is a FUNCTION, resolved per dial, because the laptop roams and an
// interface captured at startup is a snapshot that goes stale exactly when the
// network changes. Returns nil when this platform cannot bind (non-Linux), so
// the caller keeps refusing rather than measuring the wrong path.
func NewMultiProberBound(specs map[string]ServiceProbe, iface func() string) *MultiProber {
	ctrl := bindToDeviceControl(iface)
	if ctrl == nil {
		return nil
	}
	m := NewMultiProber(specs)
	d := &net.Dialer{Timeout: m.timeout, Control: ctrl}
	m.bound = d
	m.http.Client = &http.Client{
		Timeout: 5 * time.Second,
		// Same reason as everywhere else here: establishing a connection is what the
		// censor blocks, and a pooled one never presents it with a handshake.
		Transport: &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true},
	}
	return m
}
