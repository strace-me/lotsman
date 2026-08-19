package core

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/burstprobe"
	"github.com/strace-me/lotsman/pkg/dataplane"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// nodeCanaryTimeout bounds one measurement. Generous, because the thing being
// measured is a slow path by hypothesis, but bounded so a frozen exit costs one
// pass rather than the ranking loop.
const nodeCanaryTimeout = 20 * time.Second

// canaryRefusal remembers refusals we have already explained, so a permanent
// misconfiguration says its piece once instead of every pass.
var canaryRefusal sync.Map // reason -> struct{}

// canaryNodeKBps measures ONE exit's CAPACITY: point the probe selector at it and
// pull a volume we chose through the probe ingress, reporting KiB/s.
//
// This is the measurement node ranking never had. Delay is addressable (Clash
// /delay names the node) but throughput was not, so the daemon pulled bytes down
// whatever path was active and handed every candidate the same number (LOT-67).
// Passive carry fixed the addressing and answers a different question — bytes that
// crossed an exit measure DEMAND. An exit that moved 0.4 Mbps because the operator
// was reading text is not a 0.4 Mbps exit. Only a transfer we sized measures the
// path, and only through a selector we aimed does it describe THIS exit.
//
// It reuses the desync canary's machinery deliberately (volumeTargets, the burst
// probe, the live-session guard). A second measurement of the same kind is how the
// first throughput canary ended up wired to one caller and blind to steady state
// (principle 14) — there must not be two.
//
// Every refusal returns ok=false, never 0. A zero would read as "this exit carries
// nothing", which is the verdict that demotes it.
func (c *Core) canaryNodeKBps(ctx context.Context, service, node string) (float64, bool) {
	svc, ok := c.reg.Services[service]
	if !ok {
		return 0, false
	}
	// No probe ingress, no addressability: sel-probe is only emitted alongside the
	// probe-in inbound, and without -probe-proxy there is nothing to send through it.
	// Measuring anyway would go down the active path and reproduce the very defect
	// this exists to fix.
	if c.opts.ProbeProxy == "" {
		c.sayOnce("no -probe-proxy: node canary cannot address an exit, ranking stays latency-only")
		return 0, false
	}
	// Only rules with an EXPLICIT volume target. Falling back to the probe target is
	// what once measured a 204 at 0 KiB/s and demoted every recipe: a body-less
	// endpoint cannot show a volume freeze, because a volume freeze acts on bytes
	// (principle 15).
	if len(volumeTargets(svc)) == 0 {
		return 0, false
	}
	// Not while someone is playing. The pull competes with the traffic it would
	// disturb, and one uncredited measurement is cheaper than a spoiled call.
	if dataplane.RealtimeActive(ctx, c.clash) {
		return 0, false
	}

	ctx, cancel := context.WithTimeout(ctx, nodeCanaryTimeout)
	defer cancel()

	// Aim the selector, then measure. The selector is left where it is afterwards:
	// nothing but probe ingress routes through it, so there is no state to restore
	// and no live traffic to disturb.
	if err := c.clash.SetSelector(ctx, singbox.ProbeSelector, node); err != nil {
		c.log.Debug("node canary: cannot aim the probe selector", "node", node, "err", err)
		return 0, false
	}

	// TCP only, and h3=nil is the load-bearing part. The H3 client carries no proxy,
	// so a QUIC pull would leave through the ordinary path and describe a different
	// exit than the one we aimed at — a verdict about something the measurement did
	// not touch (principle 2).
	via := c.canaryListen()
	if via == "" {
		return 0, false
	}
	tcpEps, _ := splitEndpoints(volumeTargets(svc), dataplane.BurstClient(via, nodeCanaryTimeout), nil)
	if len(tcpEps) == 0 {
		return 0, false
	}
	q, said := burstprobe.ProbeEndpointsSaying(ctx, tcpEps, c.volumeBytes(svc), volumeAttempts)
	if q.Samples == 0 {
		// The pull did not happen. Distinct from a pull that delivered nothing, which
		// IS a verdict and is reported below as 0 KiB/s with ok=true.
		c.log.Debug("node canary: no measurement", "service", service, "node", node, "why", said)
		return 0, false
	}
	c.log.Info("node canary", "service", service, "node", node,
		"kbps", q.GoodputKBps, "kib", q.Bytes>>10, "said", said)
	return q.GoodputKBps, true
}

// sayOnce logs a permanent refusal the first time only. A component that declines
// must say why (principle 3), but a reason that cannot change does not become
// truer by being repeated every pass — and a line that repeats is a line the
// operator learns to skip.
func (c *Core) sayOnce(reason string) {
	if _, seen := canaryRefusal.LoadOrStore(reason, struct{}{}); seen {
		return
	}
	c.log.Warn("node canary declined", "why", reason)
}

// canaryListen is where the canary's own socks ingress lives: the probe address
// with the port moved up by one.
//
// It is DERIVED rather than configured because the alternative is a second flag
// that can be forgotten, and a canary ingress that silently shares probe-in is
// exactly the defect this exists to fix — on 2026-08-19 one route rule sent every
// service's probe out through whichever node the canary had aimed at, so `x` and
// `social` were both judged on youtube's candidate exit. One knob, two ports, no
// way to point them at the same place by accident.
func (c *Core) canaryListen() string {
	host, portStr, err := net.SplitHostPort(c.opts.ProbeProxy)
	if err != nil {
		return ""
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port >= 65535 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port+1))
}
