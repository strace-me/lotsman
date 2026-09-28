package throughput

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/strace-me/lotsman/pkg/burstprobe"
)

// Selector is the narrow surface a per-node throughput measurement needs: point
// the probe path at one node, and put it back afterwards.
//
// Both halves are required, and the second is the one that is easy to forget. The
// probe selector is SHARED with the live prober and the sandbox, so a scan that
// leaves it aimed at the node it last measured does not merely waste time — the
// next probe of any rule reports on a node the operator never chose. "Lotsman
// started misbehaving after a scan" is exactly the failure this split prevents.
type Selector interface {
	// PointAt routes the probe path through one node, returning the node that was
	// in place beforehand so the caller can restore it.
	PointAt(ctx context.Context, node string) (previous string, err error)
	// Current reports what the probe path is aimed at now.
	Current(ctx context.Context) (string, error)
}

// Prober measures a node's sustained throughput by pulling real bytes through it.
//
// It pulls bytes rather than asking, because the question is capacity and only a
// download answers it: a node under the TSPU volume freeze replies to a delay
// test in 40ms and then stops after a few tens of kilobytes. That is why passive
// per-exit carry cannot stand in (it measures demand), and why the cost of this
// is real uplink and must be budgeted rather than assumed.
type Prober struct {
	// URLs are what to download. They must be bodies with real content: a 204
	// returns nothing, and a canary scoring it at 0 KiB/s once demoted every
	// strategy ever tried.
	URLs []string
	// TargetBytes is how much to pull per node. burstprobe.DefaultTargetBytes is
	// the project-wide default.
	TargetBytes int64
	// ClientFor builds the HTTP client that routes through the given node. It is a
	// function because the client carries the routing: pointing the selector is
	// necessary but the probe must also speak on the same path.
	ClientFor func(ctx context.Context, node string) (client *http.Client, err error)
	// Settle is how long to wait after re-pointing the selector before pulling.
	// Probes issued the instant after a flip race the old path.
	Settle time.Duration
	// Restore puts the probe path back. nil means the caller relies on the
	// selector's own PointAt contract; a non-nil Restore is called after EVERY
	// measurement, including a failed one, because a failed measurement still
	// moved the selector.
	Restore func(ctx context.Context, previous string) error
}

// Measure pulls one node's throughput and ALWAYS leaves the probe path as it
// found it. The restore runs on a fresh context: the caller's context may already
// be cancelled (that is one of the ways a measurement fails), and restoring the
// probe path is exactly the moment you must not skip.
func (p *Prober) Measure(ctx context.Context, node string) (kbps float64, ok bool) {
	if p == nil || len(p.URLs) == 0 || p.ClientFor == nil {
		return 0, false
	}
	client, err := p.ClientFor(ctx, node)
	if err != nil || client == nil {
		return 0, false
	}
	if p.Settle > 0 {
		timer := time.NewTimer(p.Settle)
		select {
		case <-ctx.Done():
			timer.Stop()
			p.restore(context.WithoutCancel(ctx))
			return 0, false
		case <-timer.C:
		}
	}
	target := p.TargetBytes
	if target <= 0 {
		target = burstprobe.DefaultTargetBytes
	}
	q := burstprobe.Probe(ctx, client, p.URLs, target, 1)

	// Restore before interpreting the result: a frozen node and an abandoned
	// probe are the same shape of number, and the caller must never be left with
	// the probe path mis-aimed to find out which it was.
	p.restore(context.WithoutCancel(ctx))

	// "Known" is decided from what burstprobe actually reports, because it does
	// NOT set quality.GoodputKnown — only noderank's canary path does. Reading
	// that field here would reject every real measurement. This is the same
	// defect shape as LOT-69's BandwidthMbps: a field the vocabulary defines and
	// no producer fills.
	//
	// Short is refused on purpose: a body that ended early means the figure
	// describes the ENDPOINT's size, not the path's capacity, and recording it
	// would give a small file the power to rate a whole exit.
	if q.Samples == 0 || q.Loss >= 1 || q.Short {
		return 0, false
	}
	return q.GoodputKBps, q.GoodputKBps > 0
}

func (p *Prober) restore(ctx context.Context) {
	if p.Restore == nil {
		return
	}
	_ = p.Restore(ctx, "")
}

// FlareSelector is a Selector over a plain function pair, for the cases where the
// caller has "point at node" and "read current" closures rather than a client
// type. It exists so tests and alternate transports share one contract.
type FlareSelector struct {
	Point func(ctx context.Context, node string) (string, error)
	// Read reports what the probe path is aimed at now.
	Read func(ctx context.Context) (string, error)
}

func (f FlareSelector) PointAt(ctx context.Context, node string) (string, error) {
	return f.Point(ctx, node)
}

func (f FlareSelector) Current(ctx context.Context) (string, error) {
	if f.Read == nil {
		return "", fmt.Errorf("flareselector: no current")
	}
	return f.Read(ctx)
}

// SortNodes returns nodes in stable order, so a scan's queue is the same queue on
// every restart and coverage grows predictably rather than by map iteration.
func SortNodes(nodes []string) []string {
	out := append([]string(nil), nodes...)
	sort.Strings(out)
	return out
}
